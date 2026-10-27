// Remember which connection sections the user left open. A redirected account
// form may explicitly request its section to open, even if it was collapsed.
(function () {
  var sections = document.querySelectorAll('details.platform[id^="accounts-"]');
  if (!sections.length) return;
  var requested = new URLSearchParams(location.search).get('open');
  sections.forEach(function (section) {
    var key = 'eavesong:settings:' + section.id;
    try {
      var saved = localStorage.getItem(key);
      if (saved !== null) section.open = saved === 'open';
    } catch (e) { /* Storage may be disabled; the page still works. */ }
    if (section.id === 'accounts-' + requested) section.open = true;
    section.addEventListener('toggle', function () {
      try { localStorage.setItem(key, section.open ? 'open' : 'closed'); } catch (e) {}
    });
  });
})();

// Copy a suggested reply to the clipboard. Eavesong never posts it for you.
document.addEventListener('click', function (event) {
  var button = event.target.closest('[data-copy]');
  if (!button || !navigator.clipboard) return;
  navigator.clipboard.writeText(button.getAttribute('data-copy')).then(function () {
    var label = button.textContent;
    button.textContent = 'Copied';
    var usage = button.getAttribute('data-usage');
    if (usage) fetch(usage, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: 'event=copied'}).then(function(r) { if (!r.ok) throw new Error(); }).catch(function() { button.textContent = 'Copied; tracking failed'; });
    setTimeout(function () { button.textContent = label; }, 1500);
  });
});

// Settings: show only the fields the chosen AI provider uses.
(function () {
  var radios = document.querySelectorAll('input[name="ai_provider"]');
  if (!radios.length) return;
  var fields = document.querySelector('.provider-fields');
  var model = document.getElementById('ai_model');
  var key = document.getElementById('ai_key');
  var savedProvider = fields.getAttribute('data-saved-provider');
  var hasSavedKey = !!document.querySelector('[data-remove-key]');
  var modelValues = {}, keyValues = {};
  var previous = document.querySelector('input[name="ai_provider"]:checked').value;
  modelValues[previous] = model.value;
  keyValues[previous] = key.value;
  function update() {
    var chosen = document.querySelector('input[name="ai_provider"]:checked');
    var value = chosen ? chosen.value : 'none';
    if (value !== previous) {
      modelValues[previous] = model.value;
      keyValues[previous] = key.value;
      model.value = modelValues[value] || '';
      key.value = keyValues[value] || '';
      previous = value;
    }
    if (value !== 'none') chosen.closest('label').after(fields);
    document.querySelector('[data-saved-key-hint]').hidden = !hasSavedKey || value !== savedProvider;
    document.querySelector('[data-new-key-hint]').hidden = hasSavedKey && value === savedProvider;
    var remove = document.querySelector('[data-remove-key]');
    if (remove) {
      remove.hidden = value !== savedProvider;
      if (remove.hidden) remove.querySelector('input').checked = false;
    }
    key.placeholder = hasSavedKey && value === savedProvider ? '••••••••' : '';
    model.setAttribute('aria-describedby', value === 'anthropic' ? 'model-hint' : 'model-' + value + '-hint');
    document.querySelectorAll('[data-provider]').forEach(function (el) {
      el.hidden = el.getAttribute('data-provider').split(' ').indexOf(value) < 0;
    });
  }
  radios.forEach(function (r) { r.addEventListener('change', update); });
  update();
})();

// Account fields: each account becomes a chip. The original input stays in the
// form (hidden) and holds the comma-separated list the server reads, so the
// form still works without JavaScript.
document.querySelectorAll('input[data-chips]').forEach(function (field) {
  var box = document.createElement('div');
  box.className = 'chips';
  var list = document.createElement('ul');
  list.className = 'chip-list';
  var entry = document.createElement('input');
  entry.type = 'text';
  entry.className = 'chip-entry';
  entry.placeholder = field.placeholder;
  entry.spellcheck = false;
  entry.setAttribute('autocapitalize', 'off');
  var label = document.querySelector('label[for="' + field.id + '"]');
  entry.setAttribute('aria-label', 'Add ' + (label ? label.lastChild.textContent.trim() : '') + ' account');
  var values = field.value.split(',').map(function (v) { return v.trim(); }).filter(Boolean);

  function clean(v) {
    v = v.trim().replace(/^@/, '').replace(/^\/?u\//, '');
    var m = v.match(/(?:x\.com|twitter\.com|reddit\.com\/(?:user|u)|bsky\.app\/profile|github\.com|lobste\.rs\/(?:~|u))\/([^\/?#\s]+)/i) || v.match(/user\?id=([^&\s]+)/);
    return m ? m[1] : v;
  }
  function sync() {
    field.value = values.join(', ');
    list.textContent = '';
    values.forEach(function (v, i) {
      var li = document.createElement('li');
      li.className = 'chip';
      li.appendChild(document.createTextNode(v));
      var x = document.createElement('button');
      x.type = 'button';
      x.className = 'chip-remove';
      x.setAttribute('aria-label', 'Remove ' + v);
      x.textContent = '×';
      x.addEventListener('click', function () { values.splice(i, 1); sync(); entry.focus(); });
      li.appendChild(x);
      list.appendChild(li);
    });
    entry.placeholder = values.length ? 'Add another' : field.placeholder;
  }
  function add() {
    entry.value.split(',').forEach(function (part) {
      var v = clean(part);
      if (v && values.map(function (s) { return s.toLowerCase(); }).indexOf(v.toLowerCase()) < 0) values.push(v);
    });
    entry.value = '';
    sync();
  }
  entry.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); add(); }
    else if (e.key === 'Backspace' && !entry.value && values.length) { values.pop(); sync(); }
  });
  entry.addEventListener('blur', add);
  // Anything still typed when the form is saved is kept too.
  if (field.form) field.form.addEventListener('submit', add);
  box.addEventListener('click', function (e) { if (e.target === box || e.target === list) entry.focus(); });

  field.type = 'hidden';
  field.removeAttribute('id');
  entry.id = 'own_' + field.name.slice(4);
  box.appendChild(list);
  box.appendChild(entry);
  field.parentNode.insertBefore(box, field);
  sync();
});

// Inbox: while the page is open and visible, ask every 30 seconds whether new
// mentions arrived. Show a banner instead of reloading, so nothing moves while
// you read. After "Check now", reload once the check has finished.
(function () {
  var live = document.getElementById('live');
  if (!live) return;
  var since = live.getAttribute('data-since');
  var checked = live.getAttribute('data-checked');
  var checking = live.getAttribute('data-checking') === '1';
  var started = Date.now();
  var note = document.getElementById('new-note');
  var show = document.getElementById('new-show');
  show.addEventListener('click', function (e) {
    e.preventDefault();
    location.href = location.pathname + location.search.replace(/([?&])checking=1&?/, '$1').replace(/[?&]$/, '');
  });
  function poll() {
    if (document.visibilityState !== 'visible') return;
    fetch(live.getAttribute('data-url') + '?since=' + encodeURIComponent(since), { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (a) {
        if (!a) return;
        if (checking && !a.checking && a.last_check && a.last_check !== checked) {
          checking = false;
          show.click(); // the check you started has finished
          return;
        }
        if (a.new > 0) {
          note.querySelector('span').textContent = a.new === 1 ? '1 new mention.' : a.new + ' new mentions.';
          note.hidden = false;
        }
      })
      .catch(function () {});
  }
  setInterval(poll, checking ? 5000 : 30000);
  document.addEventListener('visibilitychange', poll);
  if (checking) {
    // A check takes a while; stop waiting after ten minutes.
    setTimeout(function () { checking = false; }, 10 * 60 * 1000);
  }
})();

// Search and Traffic tabs: when opening one started a fetch from Google or
// Bing, reload once the fetch has finished so the new numbers show.
(function () {
  var note = document.querySelector('[data-fetch-status]');
  if (!note) return;
  var started = Date.now();
  var timer = setInterval(function () {
    if (Date.now() - started > 2 * 60 * 1000) { clearInterval(timer); return; }
    fetch(note.getAttribute('data-fetch-status'), { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (s) {
        if (s && !s.fetching) {
          clearInterval(timer);
          var engine = new URLSearchParams(location.search).get('engine');
          location.replace(location.pathname + (engine ? '?engine=' + encodeURIComponent(engine) : ''));
        }
      })
      .catch(function () {});
  }, 2000);
})();

// Buttons that wait on another service (Google, the AI provider) show a
// spinner and what is happening, and can't be pressed twice.
(function () {
  document.addEventListener('submit', function (e) {
    var button = e.submitter || e.target.querySelector('button[type="submit"]');
    if (!button || !button.hasAttribute('data-busy')) return;
    if (button.classList.contains('is-busy')) { e.preventDefault(); return; }
    button.setAttribute('data-label', button.textContent);
    button.textContent = button.getAttribute('data-busy');
    button.classList.add('is-busy');
    button.setAttribute('aria-busy', 'true');
    // Disable after the form has been sent, or the browser drops the request.
    setTimeout(function () { button.disabled = true; }, 0);
  });
  // Coming back with the Back button shows the page as it was left; reset it.
  window.addEventListener('pageshow', function () {
    document.querySelectorAll('button.is-busy').forEach(function (b) {
      b.textContent = b.getAttribute('data-label');
      b.classList.remove('is-busy');
      b.removeAttribute('aria-busy');
      b.disabled = false;
    });
  });
})();
