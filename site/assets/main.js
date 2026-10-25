(() => {
  // Old single-page app links (/apps/#porchbook) now have their own pages.
  if (location.pathname.replace(/\/$/, '') === '/apps') {
    const moved = { '#porchbook': '/apps/porchbook', '#mow-book': '/apps/mow-book', '#android': '/apps/mow-book#android' };
    if (moved[location.hash]) { location.replace(moved[location.hash]); return; }
  }
  const root = document.documentElement;
  const themeButton = document.querySelector('.theme-toggle');
  const navButton = document.querySelector('.menu-toggle');
  const nav = document.querySelector('.site-nav');
  const storedTheme = (() => { try { return localStorage.getItem('gph-theme'); } catch { return null; } })();
  if (storedTheme === 'dark' || storedTheme === 'light') root.dataset.theme = storedTheme;
  const updateThemeLabel = () => {
    const dark = root.dataset.theme === 'dark';
    const themeMeta = document.querySelector('meta[name="theme-color"]');
    if (themeMeta) themeMeta.content = dark ? '#151b1a' : '#f5f3ef';
    themeButton.setAttribute('aria-label', `Switch to ${dark ? 'light' : 'dark'} theme`);
    themeButton.title = `Switch to ${dark ? 'light' : 'dark'} theme`;
  };
  updateThemeLabel();
  themeButton.addEventListener('click', () => {
    const next = root.dataset.theme === 'dark' ? 'light' : 'dark';
    root.dataset.theme = next;
    try { localStorage.setItem('gph-theme', next); } catch {}
    updateThemeLabel();
  });
  if (navButton && nav) {
    const closeNav = () => { nav.classList.remove('is-open'); navButton.setAttribute('aria-expanded', 'false'); navButton.setAttribute('aria-label', 'Open navigation'); };
    navButton.addEventListener('click', () => {
      const open = navButton.getAttribute('aria-expanded') !== 'true';
      nav.classList.toggle('is-open', open);
      navButton.setAttribute('aria-expanded', String(open));
      navButton.setAttribute('aria-label', open ? 'Close navigation' : 'Open navigation');
    });
    nav.querySelectorAll('a').forEach(link => link.addEventListener('click', closeNav));
    document.addEventListener('keydown', event => { if (event.key === 'Escape') { closeNav(); navButton.focus(); } });
    window.addEventListener('resize', () => { if (window.innerWidth > 700) closeNav(); });
  }
  const form = document.querySelector('#contact-form');
  if (form) form.addEventListener('submit', event => {
    event.preventDefault();
    const status = document.querySelector('#form-status');
    status.className = 'form-status';
    if (!form.checkValidity()) {
      form.reportValidity();
      status.textContent = 'Please complete the required fields before preparing your email.';
      status.classList.add('is-error');
      return;
    }
    const fields = new FormData(form);
    const subject = `Business inquiry from ${String(fields.get('company') || fields.get('name') || 'website visitor')}`;
    const body = `Name: ${fields.get('name')}\nBusiness email: ${fields.get('email')}\nCompany: ${fields.get('company') || 'Not provided'}\n\nMessage:\n${fields.get('message')}`;
    status.textContent = 'Your mail app should open with a draft. No message has been submitted through this website.';
    status.classList.add('is-note');
    window.location.href = `mailto:admin@gphautoads.com?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
  });
  const wl = document.querySelector('#waitlist-form');
  if (wl) wl.addEventListener('submit', async event => {
    event.preventDefault();
    const status = document.querySelector('#waitlist-status');
    const button = wl.querySelector('button');
    const email = wl.email.value.trim();
    status.className = 'form-status';
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email)) {
      status.textContent = 'Please enter a valid email address.';
      status.classList.add('is-error');
      wl.email.focus();
      return;
    }
    button.disabled = true;
    try {
      const r = await fetch('https://sign.gphdigital.com/api/waitlist', {
        method: 'POST', headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ email, app: wl.dataset.app, platform: wl.dataset.platform, website: wl.website.value, lang: (navigator.language || '').slice(0, 5) }),
      });
      if (!r.ok) throw new Error(String(r.status));
      const data = await r.json();
      status.textContent = data.already ? "You're already on the list. We'll email you at launch." : "Thanks. We'll email you once, when it's ready.";
      status.classList.add('is-note');
      wl.email.value = '';
    } catch {
      status.textContent = "That didn't go through. Please try again, or email admin@gphautoads.com.";
      status.classList.add('is-error');
    } finally {
      button.disabled = false;
    }
  });
})();
