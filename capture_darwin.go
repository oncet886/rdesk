//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework CoreGraphics -framework VideoToolbox -framework AVFoundation -framework AudioToolbox -framework Foundation
#include <ScreenCaptureKit/ScreenCaptureKit.h>
#include <VideoToolbox/VideoToolbox.h>
#import <AVFoundation/AVFoundation.h>
#include <CoreVideo/CoreVideo.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

// 持续推流的 ScreenCaptureKit 截屏（屏幕变化时系统推新帧，GPU 缩放）。两种模式：
//   gVideo=0：拷成 RGBA 给 Go 做 JPEG（兼容模式）
//   gVideo=1：直接送进 VideoToolbox 硬件 H.264 编码（低延迟码控），Go 只取编码好的包
static pthread_mutex_t gMu = PTHREAD_MUTEX_INITIALIZER;
static uint8_t *gBuf = NULL;
static int gW = 0, gH = 0;
static uint64_t gSeq = 0;
static volatile int gStopped = 0;
static int gVideo = 0;
// 诊断计数
static volatile uint64_t gCapCount = 0, gEncCount = 0, gEncLatSum = 0, gEncLatN = 0;

// ---------- H.264 ----------
typedef struct { uint8_t *data; int len; int key; int w; int h; } rdPkt;
#define QN 64
static rdPkt gQ[QN];
static int gQHead = 0, gQLen = 0;
static pthread_cond_t gQCond = PTHREAD_COND_INITIALIZER;
static VTCompressionSessionRef gEnc = NULL;
static CVPixelBufferRef gLastPB = NULL;
static int64_t gLastEncodeMs = 0, gLastPts = 0;
static int gRefine = 0, gPaused = 0, gSkipped = 0, gEncW = 0, gEncH = 0, gKbps = 6000, gFps = 30;
static volatile int gForceKey = 0;
static dispatch_source_t gTimer = nil;

static int64_t nowMs(void) { return (int64_t)(clock_gettime_nsec_np(CLOCK_UPTIME_RAW) / 1000000ULL); }

static void qClear(void) {
	pthread_mutex_lock(&gMu);
	while (gQLen > 0) { free(gQ[gQHead].data); gQHead = (gQHead + 1) % QN; gQLen--; }
	pthread_mutex_unlock(&gMu);
}

static void qPush(uint8_t *d, int len, int key) {
	pthread_mutex_lock(&gMu);
	if (gQLen == QN) { free(gQ[gQHead].data); gQHead = (gQHead + 1) % QN; gQLen--; gForceKey = 1; }
	gQ[(gQHead + gQLen) % QN] = (rdPkt){d, len, key, gEncW, gEncH};
	gQLen++;
	pthread_cond_signal(&gQCond);
	pthread_mutex_unlock(&gMu);
}

static int rdvNext(rdPkt *out, int timeoutMs) {
	pthread_mutex_lock(&gMu);
	if (gQLen == 0) {
		struct timespec ts;
		clock_gettime(CLOCK_REALTIME, &ts);
		ts.tv_nsec += (long)timeoutMs * 1000000L;
		ts.tv_sec += ts.tv_nsec / 1000000000L;
		ts.tv_nsec %= 1000000000L;
		pthread_cond_timedwait(&gQCond, &gMu, &ts);
	}
	int ok = 0;
	if (gQLen > 0) { *out = gQ[gQHead]; gQHead = (gQHead + 1) % QN; gQLen--; ok = 1; }
	pthread_mutex_unlock(&gMu);
	return ok;
}

static void rdvFree(uint8_t *p) { free(p); }

// 编码输出：转成 Annex-B（00 00 00 01 起始码），关键帧前面带上 SPS/PPS
static void encOut(void *ref, void *src, OSStatus st, VTEncodeInfoFlags fl, CMSampleBufferRef sb) {
	if (st != noErr || !sb || !CMSampleBufferDataIsReady(sb)) return;
	gEncCount++;
	{ int64_t lat = nowMs() - CMSampleBufferGetPresentationTimeStamp(sb).value; if (lat >= 0 && lat < 2000) { gEncLatSum += lat; gEncLatN++; } }
	int key = 1;
	CFArrayRef att = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (att && CFArrayGetCount(att) > 0) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(att, 0);
		CFBooleanRef ns = NULL;
		if (CFDictionaryGetValueIfPresent(d, kCMSampleAttachmentKey_NotSync, (const void **)&ns) && ns == kCFBooleanTrue) key = 0;
	}
	static const uint8_t sc[4] = {0, 0, 0, 1};
	NSMutableData *out = [NSMutableData dataWithCapacity:64 * 1024];
	CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
	size_t cnt = 0;
	int hl = 4;
	CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, 0, NULL, NULL, &cnt, &hl);
	if (key) {
		for (size_t i = 0; i < cnt; i++) {
			const uint8_t *p; size_t n;
			if (CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, i, &p, &n, NULL, NULL) == noErr) {
				[out appendBytes:sc length:4];
				[out appendBytes:p length:n];
			}
		}
	}
	CMBlockBufferRef bb = CMSampleBufferGetDataBuffer(sb);
	size_t total = CMBlockBufferGetDataLength(bb);
	uint8_t *tmp = malloc(total);
	CMBlockBufferCopyDataBytes(bb, 0, total, tmp);
	size_t off = 0;
	while (off + hl <= total) {
		uint32_t n = 0;
		for (int i = 0; i < hl; i++) n = (n << 8) | tmp[off + i];
		off += hl;
		if (off + n > total) break;
		[out appendBytes:sc length:4];
		[out appendBytes:tmp + off length:n];
		off += n;
	}
	free(tmp);
	uint8_t *d = malloc(out.length);
	memcpy(d, out.bytes, out.length);
	qPush(d, (int)out.length, key);
}

// 只在 gQueue 上调用
static void encodePB(CVPixelBufferRef pb) {
	if (!gEnc || !pb) return;
	NSDictionary *props = nil;
	if (gForceKey) { props = @{(id)kVTEncodeFrameOptionKey_ForceKeyFrame: @YES}; gForceKey = 0; }
	int64_t pts = nowMs();
	if (pts <= gLastPts) pts = gLastPts + 1;
	gLastPts = pts;
	VTCompressionSessionEncodeFrame(gEnc, pb, CMTimeMake(pts, 1000), kCMTimeInvalid, (__bridge CFDictionaryRef)props, NULL, NULL);
	gLastEncodeMs = nowMs();
}

static void destroyEncoder(void) {
	if (gEnc) {
		VTCompressionSessionCompleteFrames(gEnc, kCMTimeInvalid);
		VTCompressionSessionInvalidate(gEnc);
		CFRelease(gEnc);
		gEnc = NULL;
	}
}

static int createEncoder(int w, int h) {
	destroyEncoder();
	NSDictionary *spec = @{(id)kVTVideoEncoderSpecification_EnableLowLatencyRateControl: @YES};
	OSStatus st = VTCompressionSessionCreate(NULL, w, h, kCMVideoCodecType_H264, (__bridge CFDictionaryRef)spec, NULL, NULL, encOut, NULL, &gEnc);
	if (st != noErr) st = VTCompressionSessionCreate(NULL, w, h, kCMVideoCodecType_H264, NULL, NULL, NULL, encOut, NULL, &gEnc);
	if (st != noErr) return (int)st;
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
	if (VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_ConstrainedHigh_AutoLevel) != noErr)
		VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Main_AutoLevel);
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
	// 只参考前 1 帧、不延迟出帧：码流里声明的解码缓冲就是 1，安卓（高通/三星）解码器就不会扣着帧不显示
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_MaxFrameDelayCount, (__bridge CFNumberRef)@0);
	if (@available(macOS 13.0, *)) VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_ReferenceBufferCount, (__bridge CFNumberRef)@1);
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_AverageBitRate, (__bridge CFNumberRef)@(gKbps * 1000));
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_ExpectedFrameRate, (__bridge CFNumberRef)@(gFps));
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_MaxKeyFrameInterval, (__bridge CFNumberRef)@(gFps * 120));
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, (__bridge CFNumberRef)@(120));
	VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_DataRateLimits, (__bridge CFArrayRef)@[@(gKbps * 1000 / 8 * 3 / 2), @1]);
	VTCompressionSessionPrepareToEncodeFrames(gEnc);
	gEncW = w; gEncH = h;
	gForceKey = 1;
	return 0;
}

// ---------- 声音：系统声音 → AAC-LC 48kHz 立体声 96kbps ----------
typedef struct { uint8_t *data; int len; } rdAPkt;
#define AQN 256
static rdAPkt gAQ[AQN];
static int gAQHead = 0, gAQLen = 0;
static pthread_mutex_t gAMu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t gAQCond = PTHREAD_COND_INITIALIZER;
static AVAudioConverter *gAConv = nil;
static AVAudioFormat *gAIn = nil, *gAOut = nil;
static dispatch_queue_t gAQueue = nil;
static volatile uint64_t gAudioCount = 0;
static volatile float gAudioPeak = 0; // 诊断：最近的声音电平

static void aqPush(const void *p, int n) {
	uint8_t *d = malloc(n);
	memcpy(d, p, n);
	pthread_mutex_lock(&gAMu);
	if (gAQLen == AQN) { free(gAQ[gAQHead].data); gAQHead = (gAQHead + 1) % AQN; gAQLen--; }
	gAQ[(gAQHead + gAQLen) % AQN] = (rdAPkt){d, n};
	gAQLen++;
	pthread_cond_signal(&gAQCond);
	pthread_mutex_unlock(&gAMu);
}

static void aqClear(void) {
	pthread_mutex_lock(&gAMu);
	while (gAQLen > 0) { free(gAQ[gAQHead].data); gAQHead = (gAQHead + 1) % AQN; gAQLen--; }
	pthread_mutex_unlock(&gAMu);
}

static int rdaNext(rdAPkt *out, int timeoutMs) {
	pthread_mutex_lock(&gAMu);
	if (gAQLen == 0) {
		struct timespec ts;
		clock_gettime(CLOCK_REALTIME, &ts);
		ts.tv_nsec += (long)timeoutMs * 1000000L;
		ts.tv_sec += ts.tv_nsec / 1000000000L;
		ts.tv_nsec %= 1000000000L;
		pthread_cond_timedwait(&gAQCond, &gAMu, &ts);
	}
	int ok = 0;
	if (gAQLen > 0) { *out = gAQ[gAQHead]; gAQHead = (gAQHead + 1) % AQN; gAQLen--; ok = 1; }
	pthread_mutex_unlock(&gAMu);
	return ok;
}

static void handleAudio(CMSampleBufferRef sb) {
	CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sb);
	const AudioStreamBasicDescription *asbd = fmt ? CMAudioFormatDescriptionGetStreamBasicDescription(fmt) : NULL;
	if (!asbd) return;
	if (!gAConv || gAIn.sampleRate != asbd->mSampleRate || gAIn.channelCount != asbd->mChannelsPerFrame) {
		gAIn = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
		AudioStreamBasicDescription o = {0};
		o.mSampleRate = 48000;
		o.mFormatID = kAudioFormatMPEG4AAC;
		o.mChannelsPerFrame = 2;
		o.mFramesPerPacket = 1024;
		gAOut = [[AVAudioFormat alloc] initWithStreamDescription:&o];
		gAConv = [[AVAudioConverter alloc] initFromFormat:gAIn toFormat:gAOut];
		gAConv.bitRate = 96000;
	}
	CMItemCount n = CMSampleBufferGetNumSamples(sb);
	if (n <= 0) return;
	AVAudioPCMBuffer *pcm = [[AVAudioPCMBuffer alloc] initWithPCMFormat:gAIn frameCapacity:(AVAudioFrameCount)n];
	pcm.frameLength = (AVAudioFrameCount)n;
	OSStatus cst = CMSampleBufferCopyPCMDataIntoAudioBufferList(sb, 0, (int32_t)n, pcm.mutableAudioBufferList);
	if (getenv("RDESK_ADEBUG")) {
		static int dbg = 0;
		if (dbg++ % 50 == 0) {
			float pk = 0;
			if (pcm.floatChannelData) for (int c = 0; c < (int)gAIn.channelCount; c++) for (CMItemCount i = 0; i < n; i++) pk = fmaxf(pk, fabsf(pcm.floatChannelData[c][i]));
			fprintf(stderr, "[audio] st=%d n=%ld rate=%.0f ch=%u flags=0x%x bits=%u peak=%f\n", (int)cst, (long)n, asbd->mSampleRate, asbd->mChannelsPerFrame, asbd->mFormatFlags, asbd->mBitsPerChannel, pk);
		}
	}
	if (cst != noErr) return;
	if (pcm.floatChannelData) {
		float pk = 0;
		for (CMItemCount i = 0; i < n; i++) pk = fmaxf(pk, fabsf(pcm.floatChannelData[0][i]));
		if (pk > gAudioPeak) gAudioPeak = pk;
	}
	__block AVAudioPCMBuffer *pending = pcm;
	for (int guard = 0; guard < 16; guard++) {
		AVAudioCompressedBuffer *out = [[AVAudioCompressedBuffer alloc] initWithFormat:gAOut packetCapacity:8 maximumPacketSize:MAX(gAConv.maximumOutputPacketSize, 1536)];
		NSError *err = nil;
		AVAudioConverterOutputStatus st = [gAConv convertToBuffer:out error:&err withInputFromBlock:^AVAudioBuffer *(AVAudioPacketCount cnt, AVAudioConverterInputStatus *status) {
			if (pending) { *status = AVAudioConverterInputStatus_HaveData; AVAudioPCMBuffer *b = pending; pending = nil; return b; }
			*status = AVAudioConverterInputStatus_NoDataNow;
			return nil;
		}];
		for (AVAudioPacketCount i = 0; i < out.packetCount; i++) {
			AudioStreamPacketDescription d = out.packetDescriptions[i];
			aqPush((const uint8_t *)out.data + d.mStartOffset, (int)d.mDataByteSize);
			gAudioCount++;
		}
		if (st != AVAudioConverterOutputStatus_HaveData || out.packetCount == 0) break;
	}
}

@interface RDOut : NSObject <SCStreamOutput, SCStreamDelegate>
@end
@implementation RDOut
- (void)stream:(SCStream *)s didOutputSampleBuffer:(CMSampleBufferRef)sb ofType:(SCStreamOutputType)type {
	if (type == SCStreamOutputTypeAudio) { handleAudio(sb); return; }
	if (type != SCStreamOutputTypeScreen) return;
	CFArrayRef atts = CMSampleBufferGetSampleAttachmentsArray(sb, false);
	if (atts && CFArrayGetCount(atts) > 0) {
		NSDictionary *d = (__bridge NSDictionary *)CFArrayGetValueAtIndex(atts, 0);
		NSNumber *st = d[SCStreamFrameInfoStatus];
		if (st && st.integerValue != SCFrameStatusComplete) return; // 屏幕没变化的"空闲帧"直接跳过
	}
	CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
	if (!pb) return;
	gCapCount++;
	if (gVideo) {
		if (gLastPB) CVPixelBufferRelease(gLastPB);
		gLastPB = CVPixelBufferRetain(pb);
		gRefine = 0;
		if (gPaused) { gSkipped = 1; return; } // 网络跟不上：先不编码，恢复后编最新一帧
		encodePB(pb);
		return;
	}
	CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
	int w = (int)CVPixelBufferGetWidth(pb), h = (int)CVPixelBufferGetHeight(pb);
	size_t stride = CVPixelBufferGetBytesPerRow(pb);
	const uint8_t *src = CVPixelBufferGetBaseAddress(pb);
	pthread_mutex_lock(&gMu);
	if (w != gW || h != gH) { free(gBuf); gBuf = malloc((size_t)w * h * 4); gW = w; gH = h; }
	for (int y = 0; y < h; y++) {
		const uint8_t *s = src + y * stride;
		uint8_t *d = gBuf + (size_t)y * w * 4;
		for (int x = 0; x < w; x++, s += 4, d += 4) { d[0] = s[2]; d[1] = s[1]; d[2] = s[0]; d[3] = 255; } // BGRA → RGBA
	}
	gSeq++;
	pthread_mutex_unlock(&gMu);
	CVPixelBufferUnlockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
}
- (void)stream:(SCStream *)s didStopWithError:(NSError *)e { gStopped = 1; }
@end

static SCStream *gStream = nil;
static SCContentFilter *gFilter = nil;
static SCStreamConfiguration *gCfg = nil;
static RDOut *gOut = nil;
static dispatch_queue_t gQueue = nil;

static void stopStream(void) {
	if (gTimer) { dispatch_source_cancel(gTimer); gTimer = nil; }
	if (gStream) {
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);
		[gStream stopCaptureWithCompletionHandler:^(NSError *e) { dispatch_semaphore_signal(sem); }];
		dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
		gStream = nil;
	}
	if (gQueue) dispatch_sync(gQueue, ^{
		destroyEncoder();
		if (gLastPB) { CVPixelBufferRelease(gLastPB); gLastPB = NULL; }
	});
	qClear();
}

// 返回 0 成功；1 没权限/取不到内容；2 找不到显示器；3 启动失败；其他 = 编码器错误
static int startStream(uint32_t displayID, int maxW, int fps, int video, int audio) {
	stopStream();
	if (!gOut) {
		gOut = [RDOut new];
		gQueue = dispatch_queue_create("rdesk.capture", DISPATCH_QUEUE_SERIAL);
		gAQueue = dispatch_queue_create("rdesk.audio", DISPATCH_QUEUE_SERIAL);
	}
	aqClear();
	gVideo = video;
	gFps = fps;
	gPaused = 0; gSkipped = 0; gRefine = 0;
	__block int rc = 0;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *content, NSError *err) {
		if (err || !content) { rc = 1; dispatch_semaphore_signal(sem); return; }
		SCDisplay *target = nil;
		for (SCDisplay *d in content.displays) if (d.displayID == displayID) target = d;
		if (!target) { rc = 2; dispatch_semaphore_signal(sem); return; }
		CGDisplayModeRef mode = CGDisplayCopyDisplayMode(displayID);
		size_t pw = mode ? CGDisplayModeGetPixelWidth(mode) : target.width;
		size_t ph = mode ? CGDisplayModeGetPixelHeight(mode) : target.height;
		if (mode) CGDisplayModeRelease(mode);
		size_t w = pw, h = ph;
		if ((int)w > maxW) { h = (size_t)((double)ph * maxW / pw); w = maxW; }
		w &= ~1; h &= ~1;
		if (video) {
			__block int erc = 0;
			dispatch_sync(gQueue, ^{ erc = createEncoder((int)w, (int)h); });
			if (erc) { rc = erc; dispatch_semaphore_signal(sem); return; }
		}
		// 用"排除应用"版本（排除空列表 = 所有应用），没有窗口的后台程序的声音也能录到
		SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:target excludingApplications:@[] exceptingWindows:@[]];
		SCStreamConfiguration *cfg = [SCStreamConfiguration new];
		cfg.width = w;
		cfg.height = h;
		cfg.minimumFrameInterval = CMTimeMake(1, fps);
		cfg.pixelFormat = video ? kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange : kCVPixelFormatType_32BGRA;
		cfg.showsCursor = NO; // 光标由控制端本地画（大、跟手），不编进画面
		cfg.queueDepth = 5;
		if (video && audio) {
			// 控制端要声音时才录系统声音（不录本程序自己）
			cfg.capturesAudio = YES;
			cfg.sampleRate = 48000;
			cfg.channelCount = 2;
			cfg.excludesCurrentProcessAudio = YES;
		}
		gFilter = filter;
		gCfg = cfg;
		SCStream *st = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:gOut];
		NSError *e2 = nil;
		if (![st addStreamOutput:gOut type:SCStreamOutputTypeScreen sampleHandlerQueue:gQueue error:&e2]) {
			rc = 3; dispatch_semaphore_signal(sem); return;
		}
		if (video && audio) [st addStreamOutput:gOut type:SCStreamOutputTypeAudio sampleHandlerQueue:gAQueue error:nil];
		[st startCaptureWithCompletionHandler:^(NSError *e3) {
			if (e3) rc = 3; else gStream = st;
			dispatch_semaphore_signal(sem);
		}];
	}];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) return 3;
	if (rc == 0 && video) {
		// 屏幕静止时系统不会推帧 → 手机会一直黑屏。0.3 秒内没收到第一帧就主动截一张当首帧
		dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC), gQueue, ^{
			if (gLastPB || !gFilter || !gStream) return;
			[SCScreenshotManager captureSampleBufferWithFilter:gFilter configuration:gCfg completionHandler:^(CMSampleBufferRef sb, NSError *e) {
				if (e || !sb) return;
				CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sb);
				if (!pb) return;
				CVPixelBufferRetain(pb);
				dispatch_async(gQueue, ^{
					if (!gLastPB) { gLastPB = pb; gCapCount++; if (!gPaused) encodePB(pb); }
					else CVPixelBufferRelease(pb);
				});
			}];
		});
	}
	if (rc == 0) {
		gStopped = 0;
		pthread_mutex_lock(&gMu); gSeq = 0; pthread_mutex_unlock(&gMu);
		if (video) {
			// 画面静止后补编几帧：低码率下文字会先糊，静止时再补几次就清楚了
			gTimer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, gQueue);
			dispatch_source_set_timer(gTimer, dispatch_time(DISPATCH_TIME_NOW, 0), 150 * NSEC_PER_MSEC, 20 * NSEC_PER_MSEC);
			dispatch_source_set_event_handler(gTimer, ^{
				if (!gEnc || !gLastPB || gPaused) return;
				if (gForceKey || gSkipped) { gSkipped = 0; encodePB(gLastPB); return; }
				if (nowMs() - gLastEncodeMs > 250 && gRefine < 3) { gRefine++; encodePB(gLastPB); }
			});
			dispatch_resume(gTimer);
		}
	}
	return rc;
}

static int rdStart(uint32_t displayID, int maxW, int fps) { return startStream(displayID, maxW, fps, 0, 0); }
static int rdvStart(uint32_t displayID, int maxW, int fps, int kbps, int audio) { gKbps = kbps; return startStream(displayID, maxW, fps, 1, audio); }
static void rdStop(void) { stopStream(); }

static void rdvKeyframe(void) {
	if (!gQueue) return;
	dispatch_async(gQueue, ^{ gForceKey = 1; if (gLastPB && !gPaused) encodePB(gLastPB); });
}

// 运行中改码率（自适应码率用）
static void rdvSetBitrate(int kbps) {
	if (!gQueue) return;
	dispatch_async(gQueue, ^{
		gKbps = kbps;
		if (!gEnc) return;
		VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_AverageBitRate, (__bridge CFNumberRef)@(kbps * 1000));
		VTSessionSetProperty(gEnc, kVTCompressionPropertyKey_DataRateLimits, (__bridge CFArrayRef)@[@(kbps * 1000 / 8 * 3 / 2), @1]);
	});
}

static void rdvPause(int p) {
	if (!gQueue) return;
	dispatch_async(gQueue, ^{
		gPaused = p;
		if (!p && gSkipped && gLastPB) { gSkipped = 0; encodePB(gLastPB); }
	});
}

static void rdAudioStats(uint64_t *n, float *pk) { *n = gAudioCount; *pk = gAudioPeak; gAudioPeak = 0; }

static void rdCounters(uint64_t *c, uint64_t *e, uint64_t *ls, uint64_t *ln) { *c = gCapCount; *e = gEncCount; *ls = gEncLatSum; *ln = gEncLatN; }

static uint64_t rdSeq(void) { pthread_mutex_lock(&gMu); uint64_t s = gSeq; pthread_mutex_unlock(&gMu); return s; }
static int rdStopped(void) { return gStopped; }

// 把最新一帧拷进 dst（容量 cap 字节），返回序号；尺寸写入 w/h
static uint64_t rdCopy(uint8_t *dst, size_t cap, int *w, int *h) {
	pthread_mutex_lock(&gMu);
	*w = gW; *h = gH;
	uint64_t s = gSeq;
	if (dst && gBuf && (size_t)gW * gH * 4 <= cap) memcpy(dst, gBuf, (size_t)gW * gH * 4);
	pthread_mutex_unlock(&gMu);
	return s;
}

static uint32_t rdDisplayID(int index) {
	CGDirectDisplayID ids[16];
	uint32_t n = 0;
	CGGetActiveDisplayList(16, ids, &n);
	return index < (int)n ? ids[index] : CGMainDisplayID();
}
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
)

// ---------------- JPEG 兼容模式 ----------------

type streamCapturer struct {
	display, maxW, fps int
	started            bool
	lastSeq            uint64
	fallback           bool
}

func newCapturer() capturer { return &streamCapturer{} }

func (c *streamCapturer) configure(display, maxW, fps int) error {
	c.display, c.maxW, c.fps = display, maxW, fps
	c.fallback = false
	switch C.rdStart(C.rdDisplayID(C.int(display)), C.int(maxW), C.int(fps)) {
	case 0:
		c.started = true
		c.lastSeq = 0
		return nil
	case 1:
		c.started = false
		return errors.New("没有屏幕录制权限")
	default:
		// 推流启动不了就退回逐张截图
		c.started = false
		c.fallback = true
		return nil
	}
}

func (c *streamCapturer) grab() (*image.RGBA, bool, error) {
	if c.fallback {
		img, err := screenshot.CaptureRect(screenshot.GetDisplayBounds(c.display))
		if err != nil {
			return nil, false, err
		}
		return downscale(img, c.maxW), true, nil
	}
	if !c.started || C.rdStopped() != 0 {
		if err := c.configure(c.display, c.maxW, c.fps); err != nil {
			return nil, false, err
		}
	}
	if uint64(C.rdSeq()) == c.lastSeq {
		return nil, false, nil // 屏幕没变
	}
	var w, h C.int
	C.rdCopy(nil, 0, &w, &h)
	if w == 0 || h == 0 {
		return nil, false, nil
	}
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	seq := C.rdCopy((*C.uint8_t)(unsafe.Pointer(&img.Pix[0])), C.size_t(len(img.Pix)), &w, &h)
	if int(w)*int(h)*4 != len(img.Pix) {
		return nil, false, nil // 恰好尺寸变了，下一轮再取
	}
	c.lastSeq = uint64(seq)
	return img, true, nil
}

func (c *streamCapturer) close() {
	if c.started {
		C.rdStop()
		c.started = false
	}
}

// ---------------- H.264 硬件编码 ----------------

func videoAvailable() bool { return true }

func videoStart(display, maxW, fps, kbps int, audio bool) error {
	switch rc := C.rdvStart(C.rdDisplayID(C.int(display)), C.int(maxW), C.int(fps), C.int(kbps), map[bool]C.int{true: 1, false: 0}[audio]); rc {
	case 0:
		return nil
	case 1:
		return errors.New("没有屏幕录制权限")
	default:
		return fmt.Errorf("启动硬件编码失败 (%d)", int(rc))
	}
}

func videoNext(timeout time.Duration) (data []byte, key bool, w, h int, ok bool) {
	var p C.rdPkt
	if C.rdvNext(&p, C.int(timeout/time.Millisecond)) == 0 {
		return nil, false, 0, 0, false
	}
	data = C.GoBytes(unsafe.Pointer(p.data), p.len)
	C.rdvFree(p.data)
	return data, p.key != 0, int(p.w), int(p.h), true
}

func videoCounters() (capN, encN, latSum, latN uint64) {
	var c, e, ls, ln C.uint64_t
	C.rdCounters(&c, &e, &ls, &ln)
	return uint64(c), uint64(e), uint64(ls), uint64(ln)
}

// 下一个 AAC 包（裸 AAC-LC，48kHz 立体声，每包 1024 采样）
func audioNext(timeout time.Duration) ([]byte, bool) {
	var p C.rdAPkt
	if C.rdaNext(&p, C.int(timeout/time.Millisecond)) == 0 {
		return nil, false
	}
	b := C.GoBytes(unsafe.Pointer(p.data), p.len)
	C.rdvFree(p.data)
	return b, true
}

// 声音诊断：累计包数、最近电平（读完清零）
func audioStats() (uint64, float64) {
	var n C.uint64_t
	var pk C.float
	C.rdAudioStats(&n, &pk)
	return uint64(n), float64(pk)
}

func videoStopped() bool    { return C.rdStopped() != 0 }
func videoKeyframe()        { C.rdvKeyframe() }
func videoSetBitrate(k int) { C.rdvSetBitrate(C.int(k)) }
func videoPause(p bool)     { C.rdvPause(map[bool]C.int{true: 1, false: 0}[p]) }
func videoStop()            { C.rdStop() }
