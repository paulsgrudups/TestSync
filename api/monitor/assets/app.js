/*
 * TestSync run console. Vanilla JS, no build step, no dependencies.
 *
 * Everything the server sends is treated as untrusted text: nodes are built
 * with createElement and filled with textContent, and no HTML-parsing sink is
 * ever handed a server string. Checkpoint identifiers come from the agents and
 * will contain hostile input eventually.
 *
 * The console also mutates: it can force-release a barrier, delete a run,
 * disconnect an agent and replace a stored payload. Two rules hold for all
 * four. Every one of them confirms first and says what it costs, in agents and
 * in bytes. And every one of them may be missing from the server this page is
 * talking to — the endpoints were added after the read-only monitor, so a 404
 * or a 405 disables the action and explains itself instead of failing.
 */

(function () {
  'use strict';

  var API = '/api/v1/runs';
  var TESTS = '/tests';

  // How much of a stored payload is decoded for the viewer. A payload can be
  // megabytes; the viewer is for recognising one, not for reading it whole.
  var PREVIEW_BYTES = 8192;

  // The server trims a release reason and rejects anything longer (contract
  // section 1). The same bound is applied here so the operator learns about it
  // while typing rather than from a 400.
  var MAX_REASON_BYTES = 128;

  // Features that only newer servers have. A value of false means the endpoint
  // answered 404/405 and the matching affordance is disabled.
  var FEATURES = {
    release: 'force-release a checkpoint',
    deleteRun: 'delete a run',
    disconnect: 'disconnect an agent',
    readData: 'read a stored payload',
    writeData: 'replace a stored payload'
  };

  var state = {
    route: { name: 'list', id: null },
    intervalMs: 2000,
    paused: false,
    inFlight: false,
    lastSuccess: null,   // Date of the last good response
    error: null,         // string shown in the banner
    payload: null,       // last decoded body for the current route
    signature: null,     // JSON of the last rendered payload plus view state
    waitingSince: {},    // key -> ms timestamp of the first waiting sighting

    filter: 'all',       // one of all | waiting | idle | data
    query: '',           // run ID substring from the search box
    selected: [],        // test_ids ticked in the run table
    caps: {},            // feature -> false once the server says it lacks it
    aftermath: null,     // { text, kind } strip shown above the table
    peek: null,          // test_id whose payload summary sits under the table

    // Per-run view state, thrown away whenever the route changes.
    detail: emptyDetailState()
  };

  function emptyDetailState() {
    return {
      previewOpen: false,
      preview: null,       // { text, bytes, truncated, binary } or null
      previewError: null,
      confirmDelete: false,
      deleted: false,
      deletedNote: '',
      released: {}         // identifier -> { generation, reason, released }
    };
  }

  // liveNodes are re-stamped every second so that durations keep counting
  // without a re-render, which would otherwise steal keyboard focus.
  var liveNodes = [];
  var timer = null;

  var dom = {};

  // The open dialog, if any. It is a builder function plus its own local state;
  // refreshDialog() re-runs the builder in place.
  var dialog = { build: null, restore: null, restoreKey: null, wide: false };

  /* ---------------- small helpers ---------------- */

  function el(tag, props, children) {
    var node = document.createElement(tag);

    if (props) {
      Object.keys(props).forEach(function (key) {
        if (key === 'text') {
          node.textContent = props[key];
        } else if (key === 'class') {
          node.className = props[key];
        } else if (props[key] !== null && props[key] !== undefined) {
          node.setAttribute(key, props[key]);
        }
      });
    }

    (children || []).forEach(function (child) {
      if (child) {
        node.appendChild(child);
      }
    });

    return node;
  }

  function text(value) {
    return document.createTextNode(value);
  }

  function clear(node) {
    while (node.firstChild) {
      node.removeChild(node.firstChild);
    }
  }

  function plural(count, one, many) {
    return count === 1 ? one : many;
  }

  function fmtDuration(seconds) {
    var total = Math.max(0, Math.round(seconds));

    if (total < 60) {
      return total + 's';
    }

    var minutes = Math.floor(total / 60);
    if (minutes < 60) {
      return minutes + 'm ' + pad(total % 60) + 's';
    }

    var hours = Math.floor(minutes / 60);
    return hours + 'h ' + pad(minutes % 60) + 'm';
  }

  function pad(value) {
    return value < 10 ? '0' + value : String(value);
  }

  function fmtBytes(bytes) {
    if (bytes < 1024) {
      return bytes + ' B';
    }
    if (bytes < 1024 * 1024) {
      return (bytes / 1024).toFixed(1) + ' KiB';
    }
    return (bytes / (1024 * 1024)).toFixed(1) + ' MiB';
  }

  // startedAt maps the server's age of a run onto the browser's clock, so a
  // skewed server clock cannot produce a run that started in the future.
  function startedAt(run) {
    return Date.now() - Math.max(0, run.age_seconds) * 1000;
  }

  function fmtClock(iso) {
    var date = new Date(iso);
    return isNaN(date.getTime()) ? String(iso) : date.toLocaleString();
  }

  // live registers a node whose text is a duration counted from `since`.
  function live(node, since, render) {
    liveNodes.push({ node: node, since: since, render: render });
    node.textContent = render((Date.now() - since) / 1000);
    return node;
  }

  // explained marks an error whose message is safe to show to an operator.
  function explained(message) {
    var err = new Error(message);
    err.explained = true;
    return err;
  }

  function badge(kind, label) {
    return el('span', { class: 'badge badge-' + kind, text: label });
  }

  function byteLength(value) {
    if (window.TextEncoder) {
      return new window.TextEncoder().encode(value).length;
    }

    // Older engines: unescape(encodeURIComponent()) is the classic UTF-8 count.
    return window.unescape(window.encodeURIComponent(value)).length;
  }

  /* ---------------- icons ---------------- */

  // Icons are drawn rather than fetched: the page may not make a request the
  // server does not answer, and createElementNS keeps them out of any
  // HTML-parsing sink.
  var SVG_NS = 'http://www.w3.org/2000/svg';

  var ICONS = {
    release: [['path', 'M4 5l5 5-5 5'], ['path', 'M11 5l5 5-5 5']],
    trash: [
      ['path', 'M4 6h12'],
      ['path', 'M8 6V4h4v2'],
      ['path', 'M6 6l.7 9.3A1 1 0 0 0 7.7 16h4.6a1 1 0 0 0 1-.7L14 6']
    ],
    eye: [
      ['path', 'M3 10s2.6-4.5 7-4.5S17 10 17 10s-2.6 4.5-7 4.5S3 10 3 10Z'],
      ['circle', '10 10 1.8']
    ],
    check: [['path', 'M4 10.5l4 4 8-9']],
    close: [['path', 'M5.5 5.5l9 9'], ['path', 'M14.5 5.5l-9 9']],
    warn: [
      ['path', 'M10 8v3.4'],
      ['dot', '10 14 0.6'],
      ['path', 'M10 3.2 2.8 16.2h14.4Z']
    ],
    info: [
      ['path', 'M10 9.2v4.4'],
      ['dot', '10 6.4 0.7'],
      ['circle', '10 10 7.6']
    ]
  };

  function icon(name, size, weight) {
    var svg = document.createElementNS(SVG_NS, 'svg');

    svg.setAttribute('width', String(size || 14));
    svg.setAttribute('height', String(size || 14));
    svg.setAttribute('viewBox', '0 0 20 20');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', String(weight || 1.8));
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');

    (ICONS[name] || []).forEach(function (shape) {
      var node;

      if (shape[0] === 'path') {
        node = document.createElementNS(SVG_NS, 'path');
        node.setAttribute('d', shape[1]);
      } else {
        var parts = shape[1].split(' ');
        node = document.createElementNS(SVG_NS, 'circle');
        node.setAttribute('cx', parts[0]);
        node.setAttribute('cy', parts[1]);
        node.setAttribute('r', parts[2]);

        if (shape[0] === 'dot') {
          node.setAttribute('fill', 'currentColor');
        }
      }

      svg.appendChild(node);
    });

    return svg;
  }

  /* ---------------- buttons ---------------- */

  // button builds one clickable control. `spec.key` survives a re-render: the
  // poll loop repaints the table, and without it the keyboard would be dumped
  // back at the top of the document mid-task.
  function button(spec) {
    var node = el('button', {
      type: 'button',
      class: spec.class || 'btn',
      title: spec.title || null,
      'aria-label': spec.label || null,
      'aria-pressed': spec.pressed || null,
      'aria-checked': spec.checked || null,
      role: spec.role || null,
      'data-focus-key': spec.key || null
    });

    if (spec.icon) {
      node.appendChild(icon(spec.icon, spec.iconSize || 14, spec.iconWeight));
    }

    if (spec.text) {
      node.appendChild(text(spec.text));
    }

    if (spec.disabled) {
      node.setAttribute('disabled', 'disabled');
    } else if (spec.onClick) {
      node.addEventListener('click', spec.onClick);
    }

    return node;
  }

  function tick() {
    var mark = icon('check', 13, 2.6);
    mark.setAttribute('class', 'tick');
    return mark;
  }

  function checkbox(spec) {
    var node = button({
      class: 'chk',
      role: 'checkbox',
      checked: spec.checked,
      label: spec.label,
      key: spec.key,
      onClick: spec.onClick
    });

    node.appendChild(tick());
    return node;
  }

  function noteStrip(kind, message) {
    return el('div', { class: 'note-strip ' + kind }, [
      icon(kind === 'info' ? 'info' : 'warn', 14, 1.9),
      el('p', { text: message })
    ]);
  }

  function impact(rows) {
    return el('div', { class: 'impact' }, rows.map(function (row) {
      return el('div', { class: 'impact-row' }, [
        el('span', { class: 'k', text: row[0] }),
        el('span', { class: 'v', text: row[1] })
      ]);
    }));
  }

  /* ---------------- HTTP ---------------- */

  // request performs one call and always resolves: a rejected promise here
  // would surface as an unhandled error in a click handler.
  //
  // The resolved value is { ok: true, response } or a failure describing what
  // an operator should be told, with `kind` set to "unsupported" when the
  // server has no such route at all.
  function request(method, url, options) {
    var opts = options || {};
    var init = {
      method: method,
      cache: 'no-store',
      credentials: 'same-origin',
      headers: {}
    };

    if (opts.accept) {
      init.headers.Accept = opts.accept;
    }

    if (opts.contentType) {
      init.headers['Content-Type'] = opts.contentType;
    }

    if (opts.body !== undefined && opts.body !== null) {
      init.body = opts.body;
    }

    return window.fetch(url, init).then(function (response) {
      if (response.ok) {
        return { ok: true, response: response, status: response.status };
      }

      return describeFailure(response);
    }, function () {
      // A transport failure arrives as a TypeError whose message ("Failed to
      // fetch") is browser jargon, so only our own wording is shown.
      return {
        ok: false,
        kind: 'offline',
        status: 0,
        reason: null,
        message: 'Cannot reach the server.'
      };
    });
  }

  // describeFailure reads the error body and decides whether this server
  // understood the request at all.
  //
  // The contract gives every 4xx/5xx a machine-readable `reason`. A server
  // built before run management has no such route, so its 404 comes from the
  // router as plain text and its 405 has no body: no `reason` on a 404/405 is
  // what "this endpoint does not exist here" looks like from the browser.
  function describeFailure(response) {
    return response.text().then(function (raw) {
      var body = null;

      try {
        body = JSON.parse(raw);
      } catch (err) {
        body = null;
      }

      var reason = body && typeof body.reason === 'string' ? body.reason : null;
      var prose = body && typeof body.error === 'string' ? body.error : null;
      var kind = 'server';

      if (response.status === 401) {
        kind = 'auth';
      } else if (!reason && (response.status === 404 || response.status === 405)) {
        kind = 'unsupported';
      }

      return {
        ok: false,
        kind: kind,
        status: response.status,
        reason: reason,
        message: failureMessage(response.status, reason, prose, kind)
      };
    });
  }

  function failureMessage(status, reason, prose, kind) {
    if (kind === 'auth') {
      return 'Not authorized. Reload the page to sign in again.';
    }

    if (kind === 'unsupported') {
      return 'This server does not have that endpoint.';
    }

    switch (reason) {
      case 'run_not_found':
        return 'That run is no longer on the server. It may have finished and ' +
          'been cleaned up.';
      case 'checkpoint_not_found':
        return 'The server has never seen that checkpoint on this run.';
      case 'no_round_in_progress':
        return 'Nothing is waiting on that barrier any more — the round ended ' +
          'on its own.';
      case 'connection_not_found':
        return 'That agent is no longer attached to the run.';
      case 'data_not_found':
        return 'This run has no stored payload.';
      case 'invalid_request':
        return prose || 'The server rejected the request as invalid.';
      default:
        break;
    }

    if (status === 413) {
      return 'The replacement is larger than the server\'s ' +
        'limits.max_data_bytes cap, so nothing was written.';
    }

    return prose || 'The server answered ' + status + '.';
  }

  // markUnsupported records that a feature is missing from this server and
  // repaints, so the affordance disables itself everywhere at once.
  function markUnsupported(feature) {
    if (state.caps[feature] === false) {
      return;
    }

    state.caps[feature] = false;
    renderCapabilityNote();
    invalidate();
  }

  function supported(feature) {
    return state.caps[feature] !== false;
  }

  function renderCapabilityNote() {
    var missing = Object.keys(FEATURES).filter(function (name) {
      return state.caps[name] === false;
    });

    if (missing.length === 0) {
      dom.capabilityNote.hidden = true;
      dom.capabilityNote.textContent = '';
      return;
    }

    var names = missing.map(function (name) {
      return FEATURES[name];
    });

    dom.capabilityNote.hidden = false;
    dom.capabilityNote.textContent =
      'This server is older than this console: it cannot ' +
      joinList(names) + '. Those actions are disabled. ' +
      'Everything else on this page still works.';
  }

  function joinList(items) {
    if (items.length === 1) {
      return items[0];
    }

    return items.slice(0, -1).join(', ') + ' or ' + items[items.length - 1];
  }

  function announce(message) {
    dom.actionStatus.textContent = message;
  }

  /* ---------------- routing ---------------- */

  function parseRoute() {
    var match = /^#\/run\/(\d+)$/.exec(window.location.hash);

    return match
      ? { name: 'detail', id: match[1] }
      : { name: 'list', id: null };
  }

  function onRouteChange() {
    var next = parseRoute();

    if (next.name === state.route.name && next.id === state.route.id) {
      return;
    }

    state.route = next;
    state.payload = null;
    state.signature = null;
    state.error = null;
    state.detail = emptyDetailState();
    state.peek = null;

    closeDialog();

    dom.listView.hidden = next.name !== 'list';
    dom.detailView.hidden = next.name !== 'detail';
    dom.toolbar.hidden = next.name !== 'list';

    if (next.name === 'detail') {
      dom.detailHeading.textContent = 'Run ' + next.id;
      clear(dom.detailBody);
      dom.detailSummary.textContent = 'Loading…';
    }

    poll();
  }

  /* ---------------- polling ---------------- */

  function schedule() {
    if (timer !== null) {
      window.clearTimeout(timer);
      timer = null;
    }

    if (state.paused) {
      return;
    }

    timer = window.setTimeout(poll, state.intervalMs);
  }

  function poll() {
    if (state.inFlight) {
      return;
    }

    state.inFlight = true;

    var url = state.route.name === 'detail'
      ? API + '/' + encodeURIComponent(state.route.id)
      : API;

    window.fetch(url, {
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' }
    }).then(function (response) {
      if (response.status === 401) {
        throw explained('Not authorized. Reload the page to sign in again.');
      }
      if (response.status === 404 && state.route.name === 'detail') {
        // A run this operator has just deleted is *expected* to be gone; that
        // is the outcome, not a fault.
        if (state.detail.deleted) {
          throw explained('');
        }

        throw explained(
          'Run ' + state.route.id + ' is unknown to the server. It may have ' +
          'finished and been cleaned up.'
        );
      }
      if (!response.ok) {
        throw explained('The server answered ' + response.status + '.');
      }

      return response.json();
    }).then(function (body) {
      state.payload = body;
      state.lastSuccess = new Date();
      state.error = null;
      trackWaiting(body);
      pruneSelection(body);
      render();
    }).catch(function (err) {
      var message = err && err.explained ? err.message : 'Cannot reach the server.';

      state.error = message || null;
      renderStatus();
      renderBanner();

      if (!message && state.route.name === 'detail') {
        state.payload = null;
        render();
      }
    }).then(function () {
      state.inFlight = false;
      schedule();
    });
  }

  // trackWaiting remembers when a barrier was first seen waiting. The server
  // does not timestamp a join, so this is the observer's own clock and is
  // labelled as such in the UI.
  function trackWaiting(body) {
    var seen = {};

    function mark(key, waiting) {
      seen[key] = true;

      if (!waiting) {
        delete state.waitingSince[key];
      } else if (!state.waitingSince[key]) {
        state.waitingSince[key] = Date.now();
      }
    }

    if (body.runs) {
      body.runs.forEach(function (run) {
        mark('run:' + run.test_id, run.waiting);
      });
    }

    if (body.run && body.checkpoints) {
      mark('run:' + body.run.test_id, body.run.waiting);

      body.checkpoints.forEach(function (cp) {
        mark('cp:' + body.run.test_id + ':' + cp.identifier, cp.waiting);
      });
    }
  }

  // pruneSelection drops ticks for runs that have left the server, so a bulk
  // action can never target something that is already gone.
  function pruneSelection(body) {
    if (!body.runs) {
      return;
    }

    var alive = {};
    body.runs.forEach(function (run) {
      alive[run.test_id] = true;
    });

    state.selected = state.selected.filter(function (id) {
      return alive[id];
    });

    if (state.peek !== null && !alive[state.peek]) {
      state.peek = null;
    }
  }

  /* ---------------- chrome ---------------- */

  function renderStatus() {
    var cls = 'feed';
    var label;

    if (state.error) {
      cls += ' error';
      label = 'No contact with server';
    } else if (state.paused) {
      cls += ' paused';
      label = 'Paused';
    } else {
      cls += ' live';
      label = 'Live';
    }

    dom.feed.className = cls;

    if (state.lastSuccess) {
      var suffix = text('');
      clear(dom.feedText);
      dom.feedText.appendChild(text(label + ' · updated '));
      dom.feedText.appendChild(suffix);
      live(suffix, state.lastSuccess.getTime(), function (secs) {
        return fmtDuration(secs) + ' ago';
      });
    } else {
      dom.feedText.textContent = label;
    }
  }

  function renderBanner() {
    if (!state.error) {
      dom.banner.hidden = true;
      dom.banner.textContent = '';
      return;
    }

    var note = state.lastSuccess
      ? ' Last good update at ' + state.lastSuccess.toLocaleTimeString() + '.'
      : '';

    dom.banner.hidden = false;
    dom.banner.textContent = state.error + note +
      (state.paused ? '' : ' Retrying every ' + (state.intervalMs / 1000) + 's.');
  }

  function tickLiveNodes() {
    var now = Date.now();

    liveNodes = liveNodes.filter(function (entry) {
      if (!entry.node.isConnected) {
        return false;
      }

      entry.node.textContent = entry.render((now - entry.since) / 1000);
      return true;
    });
  }

  /* ---------------- rendering ---------------- */

  // invalidate forces the next render to repaint even though the server data
  // has not moved — the view state did.
  function invalidate() {
    state.signature = null;
    render();
  }

  function render() {
    // The rendered output depends on more than the server's answer, so the
    // cheap "nothing changed" check has to cover the view state too.
    var signature = JSON.stringify([
      state.payload,
      state.filter,
      state.query,
      state.selected,
      state.caps,
      state.aftermath,
      state.peek,
      state.detail.previewOpen,
      state.detail.preview && state.detail.preview.bytes,
      state.detail.previewError,
      state.detail.confirmDelete,
      state.detail.deleted,
      state.detail.released
    ]);

    renderBanner();

    if (signature === state.signature) {
      renderStatus();
      return;
    }

    state.signature = signature;
    liveNodes = [];

    keepFocus(function () {
      if (state.route.name === 'detail') {
        renderDetail(state.payload);
      } else {
        renderList(state.payload);
      }
    });

    renderStatus();
  }

  // keepFocus re-focuses whatever the operator was on before a repaint. Nodes
  // are rebuilt wholesale, so identity is carried by data-focus-key.
  function keepFocus(fn) {
    var active = document.activeElement;
    var key = active && active.getAttribute
      ? active.getAttribute('data-focus-key')
      : null;

    // A rebuilt text box would otherwise drop the caret at the end of the
    // value, which is unusable for anyone editing in the middle of a reason.
    var start = null;
    var end = null;

    if (key && active && typeof active.selectionStart === 'number') {
      start = active.selectionStart;
      end = active.selectionEnd;
    }

    fn();

    if (!key || (document.activeElement && document.activeElement !== document.body)) {
      return;
    }

    var candidates = document.querySelectorAll('[data-focus-key]');

    for (var i = 0; i < candidates.length; i++) {
      if (candidates[i].getAttribute('data-focus-key') !== key) {
        continue;
      }

      candidates[i].focus();

      if (start !== null && typeof candidates[i].setSelectionRange === 'function') {
        try {
          candidates[i].setSelectionRange(start, end);
        } catch (err) {
          // An input type that does not support selection; the focus is what
          // mattered.
        }
      }

      return;
    }
  }

  /* ---------------- list view ---------------- */

  function matchesFilter(run) {
    if (state.filter === 'waiting' && !run.waiting) {
      return false;
    }

    if (state.filter === 'idle' &&
      (run.waiting || run.active_connection_count > 0)) {
      return false;
    }

    if (state.filter === 'data' && !run.has_data) {
      return false;
    }

    var query = state.query.trim();

    return !query || String(run.test_id).indexOf(query) !== -1;
  }

  function renderChips(runs) {
    var counts = {
      all: runs.length,
      waiting: 0,
      idle: 0,
      data: 0
    };

    runs.forEach(function (run) {
      if (run.waiting) {
        counts.waiting++;
      }
      if (!run.waiting && run.active_connection_count === 0) {
        counts.idle++;
      }
      if (run.has_data) {
        counts.data++;
      }
    });

    Object.keys(dom.chips).forEach(function (name) {
      var chip = dom.chips[name];
      chip.setAttribute('aria-pressed', String(state.filter === name));
      chip.querySelector('.n').textContent = String(counts[name]);
    });
  }

  function renderList(body) {
    var runs = (body && body.runs) || [];
    var waiting = runs.filter(function (run) { return run.waiting; }).length;
    var agents = runs.reduce(function (total, run) {
      return total + run.active_connection_count;
    }, 0);

    renderChips(runs);

    dom.listSummary.textContent = runs.length === 0
      ? 'Nothing running'
      : runs.length + ' ' + plural(runs.length, 'run', 'runs') + ' · ' +
        waiting + ' waiting on a checkpoint · ' +
        agents + ' ' + plural(agents, 'agent', 'agents') + ' connected';

    clear(dom.listBody);

    if (runs.length === 0) {
      if (state.aftermath) {
        dom.listBody.appendChild(el('div', { class: 'panel list' }, [
          aftermathStrip()
        ]));
      }

      dom.listBody.appendChild(emptyPanel(
        'No test runs yet',
        'A run appears as soon as an agent connects to /register/{testID} ' +
        'or test data is stored with POST /tests/{testID}.'
      ));
      return;
    }

    var shown = runs.filter(matchesFilter);
    var panel = el('div', { class: 'panel list' });

    if (state.selected.length > 0) {
      panel.appendChild(bulkBar(runs));
    }

    if (state.aftermath) {
      panel.appendChild(aftermathStrip());
    }

    if (shown.length === 0) {
      panel.appendChild(el('div', { class: 'empty' }, [
        el('strong', { text: 'No runs match that filter' }),
        el('span', { text: 'Clear the filter, or wait for agents to register.' })
      ]));
      dom.listBody.appendChild(panel);
      return;
    }

    var allPicked = shown.every(function (run) {
      return state.selected.indexOf(run.test_id) !== -1;
    });
    var somePicked = !allPicked && shown.some(function (run) {
      return state.selected.indexOf(run.test_id) !== -1;
    });

    var head = el('tr', null, [
      el('th', { class: 'colchk', scope: 'col' }, [
        checkbox({
          checked: allPicked ? 'true' : (somePicked ? 'mixed' : 'false'),
          label: 'Select all runs',
          key: 'chk:all',
          onClick: function () {
            state.selected = allPicked ? [] : shown.map(function (run) {
              return run.test_id;
            });
            invalidate();
          }
        })
      ]),
      el('th', { text: 'Run', scope: 'col' }),
      el('th', { text: 'Status', scope: 'col' }),
      el('th', { text: 'Agents', scope: 'col' }),
      el('th', { text: 'Checkpoints', scope: 'col' }),
      el('th', { text: 'Data', scope: 'col' }),
      el('th', { text: 'Age', scope: 'col' }),
      el('th', { class: 'colact', scope: 'col' }, [
        el('span', { class: 'sr-only', text: 'Actions' })
      ])
    ]);

    var table = el('table', null, [
      el('caption', {
        class: 'sr-only',
        text: 'Test runs known to this server, newest state on every refresh'
      }),
      el('thead', null, [head]),
      el('tbody', null, shown.map(runRow))
    ]);

    panel.appendChild(el('div', { class: 'table-wrap' }, [table]));

    if (state.peek !== null) {
      var peeked = runs.filter(function (run) {
        return run.test_id === state.peek;
      })[0];

      if (peeked) {
        panel.appendChild(peekPanel(peeked));
      }
    }

    dom.listBody.appendChild(panel);
  }

  function bulkBar(runs) {
    var picked = runs.filter(function (run) {
      return state.selected.indexOf(run.test_id) !== -1;
    });
    var blocked = picked.filter(function (run) { return run.waiting; });

    var bar = el('div', { class: 'bulk' }, [
      el('strong', { text: String(picked.length) }),
      el('span', { text: plural(picked.length, 'run selected', 'runs selected') })
    ]);

    bar.appendChild(button({
      class: 'btn',
      icon: 'release',
      text: 'Force-release barriers',
      key: 'bulk:release',
      disabled: !supported('release') || blocked.length === 0,
      title: !supported('release')
        ? 'This server cannot force-release a checkpoint.'
        : (blocked.length === 0
          ? 'None of the selected runs is waiting on a barrier.'
          : 'Force-release every waiting barrier on ' + blocked.length + ' ' +
            plural(blocked.length, 'run', 'runs')),
      onClick: function () {
        startRelease(blocked.map(function (run) { return run.test_id; }));
      }
    }));

    bar.appendChild(button({
      class: 'btn danger',
      icon: 'trash',
      text: 'Delete runs',
      key: 'bulk:delete',
      disabled: !supported('deleteRun'),
      title: supported('deleteRun')
        ? 'Delete these runs and their stored payloads'
        : 'This server cannot delete a run.',
      onClick: function () {
        confirmDelete(picked);
      }
    }));

    bar.appendChild(el('span', { class: 'spacer' }));
    bar.appendChild(button({
      class: 'btn quiet',
      text: 'Clear',
      key: 'bulk:clear',
      onClick: function () {
        state.selected = [];
        invalidate();
      }
    }));

    return bar;
  }

  // aftermathStrip reports what a completed deletion cost. There is no Undo:
  // the server has no restore route, and offering one would be a lie.
  function aftermathStrip() {
    return el('div', {
      class: 'aftermath' + (state.aftermath.kind === 'bad' ? ' bad' : '')
    }, [
      el('span', { text: state.aftermath.text }),
      el('span', { class: 'spacer' }),
      button({
        class: 'btn quiet',
        text: 'Dismiss',
        key: 'aftermath:dismiss',
        onClick: function () {
          state.aftermath = null;
          invalidate();
        }
      })
    ]);
  }

  function runRow(run) {
    var picked = state.selected.indexOf(run.test_id) !== -1;

    var status = run.waiting
      ? badge('wait', 'WAITING')
      : (run.checkpoint_count > 0
        ? badge('ok', 'ALL CLEAR')
        : badge('idle', 'NO CHECKPOINTS'));

    var agents = el('td', null, [
      el('span', { text: String(run.active_connection_count) + ' active' })
    ]);

    var closed = run.connection_count - run.active_connection_count;
    if (closed > 0) {
      agents.appendChild(el('span', {
        class: 'muted',
        text: ' · ' + closed + ' closed'
      }));
    }

    var checkpointCell = el('td', null, [
      el('span', { class: run.checkpoint_count > 0 ? '' : 'muted',
        text: String(run.checkpoint_count) })
    ]);

    if (run.waiting) {
      var detail = el('div', { class: 'cell-note' });
      detail.appendChild(text(
        run.waiting_checkpoint_count + ' of ' + run.checkpoint_count + ' ' +
        plural(run.checkpoint_count, 'checkpoint', 'checkpoints') + ' blocked'
      ));

      var since = state.waitingSince['run:' + run.test_id];
      if (since) {
        detail.appendChild(text(' · seen waiting '));
        detail.appendChild(live(text(''), since, fmtDuration));
      }

      checkpointCell.appendChild(detail);
    }

    var age = el('td', { class: 'muted' });
    age.appendChild(live(el('span'), startedAt(run), fmtDuration));

    var acts = el('div', { class: 'rowacts' }, [
      button({
        class: 'iconbtn go',
        icon: 'release',
        iconSize: 15,
        key: 'row:' + run.test_id + ':release',
        label: 'Force-release the waiting barriers of run ' + run.test_id,
        disabled: !run.waiting || !supported('release'),
        title: !supported('release')
          ? 'This server cannot force-release a checkpoint.'
          : (run.waiting
            ? 'Force-release the waiting barriers'
            : 'Nothing is waiting on this run'),
        onClick: function () {
          startRelease([run.test_id]);
        }
      }),
      button({
        class: 'iconbtn',
        icon: 'eye',
        iconSize: 15,
        iconWeight: 1.7,
        key: 'row:' + run.test_id + ':peek',
        label: 'Peek at the stored payload of run ' + run.test_id,
        title: 'Peek at the stored payload',
        pressed: state.peek === run.test_id ? 'true' : 'false',
        onClick: function () {
          state.peek = state.peek === run.test_id ? null : run.test_id;
          invalidate();
        }
      }),
      button({
        class: 'iconbtn danger',
        icon: 'trash',
        iconSize: 15,
        key: 'row:' + run.test_id + ':delete',
        label: 'Delete run ' + run.test_id + ' and its data',
        disabled: !supported('deleteRun'),
        title: supported('deleteRun')
          ? 'Delete run and its data'
          : 'This server cannot delete a run.',
        onClick: function () {
          confirmDelete([run]);
        }
      })
    ]);

    var cls = (run.waiting ? 'is-waiting' : '') + (picked ? ' picked' : '');

    return el('tr', cls ? { class: cls } : null, [
      el('td', null, [
        checkbox({
          checked: picked ? 'true' : 'false',
          label: 'Select run ' + run.test_id,
          key: 'chk:' + run.test_id,
          onClick: function () {
            var at = state.selected.indexOf(run.test_id);

            if (at === -1) {
              state.selected = state.selected.concat([run.test_id]);
            } else {
              state.selected = state.selected.filter(function (id) {
                return id !== run.test_id;
              });
            }

            invalidate();
          }
        })
      ]),
      el('td', null, [
        el('a', { class: 'run-link', href: '#/run/' + run.test_id },
          [text('#' + run.test_id)])
      ]),
      el('td', null, [status]),
      agents,
      checkpointCell,
      el('td', {
        class: run.has_data ? '' : 'muted',
        text: run.has_data ? fmtBytes(run.data_size_bytes) : 'none'
      }),
      age,
      el('td', null, [acts])
    ]);
  }

  // peekPanel is the payload summary under the table. It reports what the run
  // list knows — a size — and offers the drawer, which is the only place
  // contents are ever fetched.
  function peekPanel(run) {
    var panel = el('div', { class: 'peek' }, [
      el('div', null, [
        el('div', { class: 'k', text: 'Payload' }),
        el('div', { class: 'v', text: '#' + run.test_id })
      ]),
      el('div', null, [
        el('div', { class: 'k', text: 'Size' }),
        el('div', { class: 'v', text: run.has_data ? fmtBytes(run.data_size_bytes) : '—' })
      ]),
      el('div', null, [
        el('div', { class: 'k', text: 'Stored' }),
        el('div', { class: 'v', text: run.has_data ? 'yes' : 'no' })
      ]),
      el('span', { class: 'spacer' })
    ]);

    panel.appendChild(button({
      class: 'btn',
      icon: 'eye',
      iconWeight: 1.7,
      text: 'Open payload drawer',
      key: 'peek:open',
      disabled: !supported('readData') && !supported('writeData'),
      title: (!supported('readData') && !supported('writeData'))
        ? 'This server cannot read or replace a stored payload.'
        : 'View or replace the stored payload',
      onClick: function () {
        openPayloadDrawer(run);
      }
    }));

    panel.appendChild(button({
      class: 'btn quiet',
      text: 'Close',
      key: 'peek:close',
      onClick: function () {
        state.peek = null;
        invalidate();
      }
    }));

    return panel;
  }

  /* ---------------- detail view ---------------- */

  function renderDetail(body) {
    var run = body && body.run;

    clear(dom.detailBody);

    if (state.detail.deleted) {
      dom.detailSummary.textContent = '';
      dom.detailBody.appendChild(el('div', { class: 'panel' }, [
        el('div', { class: 'done-strip' }, [
          el('span', { text: state.detail.deletedNote }),
          el('span', { class: 'spacer' }),
          el('a', { class: 'btn', href: '#/', text: 'Back to all runs' })
        ])
      ]));
      return;
    }

    if (!run) {
      dom.detailSummary.textContent = '';
      dom.detailBody.appendChild(emptyPanel(
        'Run not available',
        'The server did not return this run.'
      ));
      return;
    }

    dom.detailHeading.textContent = 'Run #' + run.test_id;
    dom.detailSummary.textContent = 'Created ' + fmtClock(run.created);

    dom.detailBody.appendChild(detailMeta(run));
    dom.detailBody.appendChild(checkpointSection(run, body.checkpoints || []));
    dom.detailBody.appendChild(agentSection(body));
    dom.detailBody.appendChild(payloadSection(body));
  }

  function detailMeta(run) {
    var statusValue = el('dd');
    statusValue.appendChild(run.waiting
      ? badge('wait', 'WAITING')
      : (run.checkpoint_count > 0 ? badge('ok', 'ALL CLEAR') : badge('idle', 'IDLE')));

    var ageValue = el('dd');
    ageValue.appendChild(live(el('span'), startedAt(run), fmtDuration));

    return el('dl', { class: 'meta' }, [
      metaCell('Status', statusValue),
      metaCell('Age', ageValue),
      metaCell('Agents', el('dd', {
        text: run.active_connection_count + ' / ' + run.connection_count
      })),
      metaCell('Checkpoints', el('dd', { text: String(run.checkpoint_count) })),
      metaCell('Waiting', el('dd', {
        class: run.waiting_checkpoint_count > 0 ? 'warn' : '',
        text: String(run.waiting_checkpoint_count)
      })),
      metaCell('Stored data', el('dd', {
        text: run.has_data ? fmtBytes(run.data_size_bytes) : 'none'
      }))
    ]);
  }

  function metaCell(label, valueNode) {
    return el('div', null, [el('dt', { text: label }), valueNode]);
  }

  function checkpointSection(run, checkpoints) {
    var section = el('section', { 'aria-labelledby': 'cp-heading' });

    var waiting = checkpoints.filter(function (cp) { return cp.waiting; });
    var idle = checkpoints.filter(function (cp) { return !cp.waiting; });

    section.appendChild(el('div', { class: 'subhead' }, [
      el('h2', { id: 'cp-heading', text: 'Checkpoints' }),
      el('p', {
        class: 'section-note',
        text: 'Force-release ends the current round for everyone waiting on it.'
      })
    ]));

    if (checkpoints.length === 0) {
      section.appendChild(emptyPanel(
        'No checkpoints yet',
        'No agent on this run has sent wait_checkpoint.'
      ));
      return section;
    }

    var list = el('ul', { class: 'checkpoints' });

    waiting.concat(idle).forEach(function (cp) {
      list.appendChild(checkpointItem(run, cp));
    });

    section.appendChild(list);
    return section;
  }

  function checkpointItem(run, cp) {
    var joined = cp.joined_count;
    var target = cp.target_count;
    var missing = Math.max(0, target - joined);
    var percent = target > 0 ? Math.min(100, Math.round((joined / target) * 100)) : 0;

    // A barrier this operator released stays marked until agents pile into it
    // again, so the outcome of the click does not vanish on the next poll.
    var released = !cp.waiting
      ? state.detail.released['cp:' + cp.identifier]
      : null;
    var kind = cp.waiting ? 'waiting' : (released ? 'released' : 'idle');

    var item = el('li', { class: 'panel checkpoint ' + kind });

    var head = el('div', { class: 'checkpoint-head' }, [
      el('div', { class: 'cp-left' }, [
        el('span', { class: 'cp-name', text: cp.identifier }),
        cp.waiting
          ? badge('wait', 'WAITING')
          : (released ? badge('ok', 'RELEASED') : badge('idle', 'IDLE'))
      ])
    ]);

    var right = el('div', { class: 'cp-left' }, [
      el('span', {
        class: 'cp-counts',
        text: joined + ' of ' + target + ' ' +
          plural(target, 'agent', 'agents') + ' joined'
      })
    ]);

    if (cp.waiting) {
      right.appendChild(button({
        class: 'btn primary',
        icon: 'release',
        iconWeight: 1.9,
        text: 'Force-release',
        key: 'cp:' + cp.identifier + ':release',
        disabled: !supported('release'),
        title: supported('release')
          ? 'End round ' + cp.generation + ' for the agents waiting here'
          : 'This server cannot force-release a checkpoint.',
        onClick: function () {
          confirmRelease([{ run: run, checkpoint: cp }]);
        }
      }));
    }

    head.appendChild(right);
    item.appendChild(head);

    if (cp.waiting && cp.generation > 1) {
      item.appendChild(el('p', {
        class: 'cp-note',
        text: 'Round ' + cp.generation + '.'
      }));
    }

    var fill = el('span');
    fill.style.width = (released ? 100 : percent) + '%';

    item.appendChild(el('div', {
      class: 'bar',
      role: 'progressbar',
      'aria-valuemin': '0',
      'aria-valuemax': String(target),
      'aria-valuenow': String(joined),
      'aria-label': 'Agents joined checkpoint ' + cp.identifier
    }, [fill]));

    var note = el('p', { class: 'cp-note' });

    if (cp.waiting) {
      var since = state.waitingSince['cp:' + run.test_id + ':' + cp.identifier];
      var lead = 'Blocked — ' + missing + ' more ' +
        plural(missing, 'agent has', 'agents have') + ' to arrive';

      if (since) {
        note.appendChild(text(lead + ', seen waiting '));
        note.appendChild(live(text(''), since, fmtDuration));
      } else {
        note.textContent = lead + '.';
      }
    } else if (released) {
      note.textContent = 'Released manually. Reason "' + released.reason +
        '" and a shared start_at were queued to the ' + released.released + ' ' +
        plural(released.released, 'agent', 'agents') + ' that ' +
        plural(released.released, 'was', 'were') + ' waiting.';
    } else if (cp.rounds_completed > 0) {
      note.textContent = cp.rounds_completed + ' ' +
        plural(cp.rounds_completed, 'round', 'rounds') +
        ' completed. Idle until the next round begins.';
    } else {
      note.textContent = 'Idle — no agent has joined yet.';
    }

    item.appendChild(note);

    if (cp.members && cp.members.length > 0) {
      item.appendChild(el('p', {
        class: 'cp-note',
        text: 'Joined by ' + cp.members.map(function (idx) {
          return 'agent #' + idx;
        }).join(', ') + '.'
      }));
    }

    return item;
  }

  // barriersOf reports which waiting barriers one agent has joined, which is
  // the blast radius of disconnecting it.
  function barriersOf(body, ordinal) {
    return (body.checkpoints || []).filter(function (cp) {
      return cp.waiting && (cp.members || []).indexOf(ordinal) !== -1;
    });
  }

  function agentSection(body) {
    var connections = body.connections || [];
    var section = el('section', { 'aria-labelledby': 'agents-heading' });

    section.appendChild(el('div', { class: 'subhead' }, [
      el('h2', { id: 'agents-heading', text: 'Agents' }),
      el('p', {
        class: 'section-note',
        text: 'Disconnecting an agent frees its slot in every barrier it joined.'
      })
    ]));

    if (connections.length === 0) {
      section.appendChild(el('div', { class: 'panel' }, [
        el('p', {
          class: 'agents-empty',
          text: 'No agents attached. Nothing is registered on /register/' +
            body.run.test_id + ' right now.'
        })
      ]));
      return section;
    }

    var list = el('ul', { class: 'agents' });

    connections.forEach(function (conn) {
      var joined = barriersOf(body, conn.index);
      var cls = 'agent' + (conn.active ? '' : ' closed') +
        (joined.length > 0 ? ' joined' : '');

      var item = el('li', {
        class: cls,
        title: joined.length > 0
          ? 'Waiting at ' + joined.length + ' ' +
            plural(joined.length, 'barrier', 'barriers')
          : (conn.active ? 'Connected' : 'Disconnected')
      }, [
        el('span', { class: 'dot', 'aria-hidden': 'true' }),
        el('span', {
          text: '#' + conn.index + (conn.active ? '' : ' closed') +
            (joined.length > 0 ? ' · waiting' : '')
        })
      ]);

      if (conn.active) {
        item.appendChild(button({
          class: 'kick',
          icon: 'close',
          iconSize: 12,
          iconWeight: 2.2,
          key: 'conn:' + conn.conn_id + ':kick',
          label: 'Disconnect agent ' + conn.index,
          disabled: !supported('disconnect'),
          title: supported('disconnect')
            ? 'Disconnect agent #' + conn.index
            : 'This server cannot disconnect an agent.',
          onClick: function () {
            confirmDisconnect(body, conn, joined);
          }
        }));
      }

      list.appendChild(item);
    });

    section.appendChild(el('div', { class: 'panel' }, [list]));
    return section;
  }

  function payloadSection(body) {
    var run = body.run;
    var section = el('section', { 'aria-labelledby': 'payload-heading' });

    section.appendChild(el('div', { class: 'subhead' }, [
      el('h2', { id: 'payload-heading', text: 'Stored payload' }),
      el('p', {
        class: 'section-note',
        text: 'The only view in the console that shows payload contents.'
      })
    ]));

    var panel = el('div', { class: 'panel' });

    var card = el('div', { class: 'payload-card' }, [
      el('div', { class: 'grow' }, [
        el('div', {
          class: 'payload-size',
          text: run.has_data ? fmtBytes(run.data_size_bytes) : 'none stored'
        }),
        el('p', {
          class: 'payload-hint',
          text: 'The server reports the size only — it does not say when the ' +
            'payload was written or which agent wrote it.'
        })
      ])
    ]);

    card.appendChild(button({
      class: 'btn',
      icon: 'eye',
      iconWeight: 1.7,
      text: state.detail.previewOpen ? 'Hide payload' : 'View payload',
      pressed: state.detail.previewOpen ? 'true' : 'false',
      key: 'payload:view',
      disabled: !supported('readData') || !run.has_data,
      title: !supported('readData')
        ? 'This server cannot read a stored payload.'
        : (run.has_data ? 'Read the stored payload' : 'Nothing is stored yet'),
      onClick: function () {
        togglePreview(run);
      }
    }));

    card.appendChild(button({
      class: 'btn',
      text: 'Replace payload',
      key: 'payload:replace',
      disabled: !supported('writeData'),
      title: supported('writeData')
        ? 'Open the payload drawer'
        : 'This server cannot replace a stored payload.',
      onClick: function () {
        openPayloadDrawer(run);
      }
    }));

    card.appendChild(button({
      class: 'btn danger',
      icon: 'trash',
      text: 'Delete run',
      key: 'payload:delete',
      disabled: !supported('deleteRun'),
      title: supported('deleteRun')
        ? 'Delete this run and its stored payload'
        : 'This server cannot delete a run.',
      onClick: function () {
        state.detail.confirmDelete = true;
        invalidate();
      }
    }));

    panel.appendChild(card);

    if (state.detail.previewError) {
      panel.appendChild(el('p', {
        class: 'wire',
        text: state.detail.previewError
      }));
    } else if (state.detail.previewOpen && state.detail.preview) {
      var preview = state.detail.preview;

      if (preview.binary) {
        panel.appendChild(el('p', {
          class: 'wire',
          text: 'This payload is not text: it contains control bytes. ' +
            fmtBytes(preview.bytes) + ' stored, not shown.'
        }));
      } else {
        panel.appendChild(el('p', { class: 'wire', text: preview.text }));
        panel.appendChild(el('p', {
          class: 'wire',
          text: preview.truncated
            ? 'Showing the first ' + fmtBytes(PREVIEW_BYTES) + ' of ' +
              fmtBytes(preview.bytes) + '.'
            : fmtBytes(preview.bytes) + ' in full.'
        }));
      }
    }

    if (state.detail.confirmDelete) {
      var attached = run.active_connection_count;
      var strip = el('div', { class: 'confirm-strip' }, [
        el('span', {
          text: 'Deleting run #' + run.test_id + ' removes its ' +
            (run.has_data ? fmtBytes(run.data_size_bytes) + ' payload' : 'stored data') +
            ' as well. ' + attached + ' ' + plural(attached, 'agent is', 'agents are') +
            ' still attached and will lose it. This cannot be undone.'
        }),
        el('span', { class: 'spacer' })
      ]);

      strip.appendChild(button({
        class: 'btn',
        text: 'Cancel',
        key: 'payload:cancel-delete',
        onClick: function () {
          state.detail.confirmDelete = false;
          invalidate();
        }
      }));

      strip.appendChild(button({
        class: 'btn danger',
        text: 'Delete run and payload',
        key: 'payload:confirm-delete',
        onClick: function () {
          runDelete([run]).then(function (result) {
            state.detail.confirmDelete = false;

            if (result.failures.length > 0) {
              state.error = result.failures[0].message;
              renderBanner();
              invalidate();
              return;
            }

            state.detail.deleted = true;
            state.detail.deletedNote = 'Run #' + run.test_id +
              ' and its payload were deleted' + droppedNote(result.dropped) +
              '. Deletion is permanent: the server has no restore.';
            announce(state.detail.deletedNote);
            invalidate();
          });
        }
      }));

      panel.appendChild(strip);
    }

    section.appendChild(panel);
    return section;
  }

  function emptyPanel(title, detail) {
    return el('div', { class: 'panel empty' }, [
      el('strong', { text: title }),
      el('span', { text: detail })
    ]);
  }

  /* ---------------- dialog shell ---------------- */

  // Dialogs live outside <main> so that the poll loop repainting the table
  // cannot tear an open one out from under the keyboard.
  function openDialog(build, wide) {
    var active = document.activeElement;

    dialog.build = build;
    dialog.wide = !!wide;
    dialog.restore = active;
    // The control that opened the dialog may itself be rebuilt by a poll while
    // the dialog is up, so its key is remembered as well as the node.
    dialog.restoreKey = active && active.getAttribute
      ? active.getAttribute('data-focus-key')
      : null;

    refreshDialog(true);
  }

  function closeDialog() {
    if (!dialog.build) {
      return;
    }

    var restore = dialog.restore;
    var key = dialog.restoreKey;

    dialog.build = null;
    dialog.restore = null;
    dialog.restoreKey = null;
    clear(dom.dialogRoot);
    setBackgroundHidden(false);

    if (restore && restore.isConnected && restore.focus) {
      restore.focus();
      return;
    }

    if (!key) {
      return;
    }

    var candidates = document.querySelectorAll('[data-focus-key]');

    for (var i = 0; i < candidates.length; i++) {
      if (candidates[i].getAttribute('data-focus-key') === key) {
        candidates[i].focus();
        return;
      }
    }
  }

  function refreshDialog(initial) {
    if (!dialog.build) {
      return;
    }

    var spec = dialog.build();

    keepFocus(function () {
      clear(dom.dialogRoot);

      var box = el('div', {
        class: 'dialog' + (dialog.wide ? ' wide' : ''),
        role: 'dialog',
        'aria-modal': 'true',
        'aria-labelledby': 'dialog-title'
      });

      box.appendChild(el('div', { class: 'dlg-head' }, [
        el('div', { class: 'dlg-icon ' + (spec.iconKind || '') },
          [icon(spec.icon || 'warn', 19, spec.iconWeight || 1.9)]),
        el('div', null, [
          el('h2', { class: 'dlg-title', id: 'dialog-title' }, spec.title),
          spec.sub ? el('p', { class: 'dlg-sub', text: spec.sub }) : null
        ])
      ]));

      (spec.beforeBody || []).forEach(function (node) {
        box.appendChild(node);
      });

      box.appendChild(el('div', { class: 'dlg-body' }, spec.body || []));
      box.appendChild(el('div', { class: 'dlg-foot' }, spec.foot || []));

      var scrim = el('div', { class: 'scrim' }, [box]);

      scrim.addEventListener('keydown', onDialogKey);
      scrim.addEventListener('mousedown', function (event) {
        // A click on the backdrop dismisses, but never while a call is in
        // flight: the operator would not learn how it ended.
        if (event.target === scrim && !spec.busy) {
          closeDialog();
        }
      });

      dom.dialogRoot.appendChild(scrim);
      setBackgroundHidden(true);
    });

    if (initial) {
      focusFirst(dom.dialogRoot);
    }
  }

  function onDialogKey(event) {
    if (event.key === 'Escape') {
      event.preventDefault();
      closeDialog();
      return;
    }

    if (event.key !== 'Tab') {
      return;
    }

    var focusable = focusableIn(dom.dialogRoot);
    if (focusable.length === 0) {
      return;
    }

    var first = focusable[0];
    var last = focusable[focusable.length - 1];

    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function focusableIn(root) {
    var nodes = root.querySelectorAll(
      'button:not([disabled]), a[href], input, textarea, select'
    );

    return Array.prototype.filter.call(nodes, function (node) {
      return node.offsetParent !== null || node === document.activeElement;
    });
  }

  function focusFirst(root) {
    var focusable = focusableIn(root);

    if (focusable.length > 0) {
      focusable[0].focus();
    }
  }

  // While a dialog is up the rest of the page is hidden from assistive tech,
  // which is what makes it modal for a screen reader as well as a mouse.
  function setBackgroundHidden(hidden) {
    [dom.topbar, dom.main, dom.footer].forEach(function (node) {
      if (!node) {
        return;
      }

      if (hidden) {
        node.setAttribute('aria-hidden', 'true');
      } else {
        node.removeAttribute('aria-hidden');
      }
    });
  }

  /* ---------------- release flow ---------------- */

  // startRelease turns a list of run IDs into concrete barriers. The run list
  // reports only counts, so the identifiers have to be fetched: they are the
  // required field of the release call and they are caller-supplied text, so
  // they are never guessed from a path.
  function startRelease(ids) {
    if (ids.length === 0) {
      return;
    }

    openDialog(function () {
      return {
        icon: 'release',
        title: [text('Reading the waiting barriers…')],
        sub: 'Fetching ' + ids.length + ' ' + plural(ids.length, 'run', 'runs') +
          ' to see which barriers are blocked.',
        busy: true,
        body: [],
        foot: [button({ class: 'btn', text: 'Cancel', onClick: closeDialog })]
      };
    });

    var targets = [];
    var failures = [];

    var chain = ids.reduce(function (previous, id) {
      return previous.then(function () {
        return request('GET', API + '/' + encodeURIComponent(id), {
          accept: 'application/json'
        }).then(function (result) {
          if (!result.ok) {
            failures.push({ id: id, message: result.message });
            return null;
          }

          return result.response.json().then(function (body) {
            (body.checkpoints || []).forEach(function (cp) {
              if (cp.waiting) {
                targets.push({ run: body.run, checkpoint: cp });
              }
            });
          }, function () {
            failures.push({ id: id, message: 'The server sent a reply this page could not read.' });
          });
        });
      });
    }, window.Promise.resolve());

    chain.then(function () {
      if (targets.length === 0) {
        openDialog(function () {
          return {
            icon: 'info',
            iconKind: 'info',
            title: [text('Nothing to release')],
            sub: failures.length > 0
              ? 'The runs could not be read.'
              : 'No barrier on the selected ' +
                plural(ids.length, 'run', 'runs') + ' is waiting any more.',
            body: failures.length > 0
              ? [impact(failures.map(function (failure) {
                return ['Run #' + failure.id, failure.message];
              }))]
              : [],
            foot: [button({ class: 'btn primary', text: 'Close', onClick: closeDialog })]
          };
        });
        return;
      }

      confirmRelease(targets, failures);
    });
  }

  // confirmRelease is the force-release dialog: what it costs, what goes on the
  // wire, and only then the button.
  function confirmRelease(targets, loadFailures) {
    var local = {
      reason: 'operator_released',
      stage: 'ask',
      busy: false,
      results: [],
      failures: (loadFailures || []).map(function (failure) {
        return { label: 'Run #' + failure.id, message: failure.message };
      })
    };

    var single = targets.length === 1 ? targets[0] : null;

    function effectiveReason() {
      return local.reason.trim() || 'operator_released';
    }

    function reasonTooLong() {
      return byteLength(local.reason.trim()) > MAX_REASON_BYTES;
    }

    function askBody() {
      var rows = [];

      if (single) {
        var cp = single.checkpoint;
        var missing = Math.max(0, cp.target_count - cp.joined_count);
        var since = state.waitingSince[
          'cp:' + single.run.test_id + ':' + cp.identifier
        ];
        var runSince = state.waitingSince['run:' + single.run.test_id];

        rows.push(['Run', '#' + single.run.test_id]);
        rows.push(['Released now', cp.joined_count + ' ' +
          plural(cp.joined_count, 'agent', 'agents')]);
        rows.push(['Never arrived', missing + ' ' +
          plural(missing, 'agent', 'agents')]);

        if (since) {
          rows.push(['Waiting for',
            fmtDuration((Date.now() - since) / 1000) + ' (observed here)']);
        } else if (runSince) {
          rows.push(['Run blocked for',
            fmtDuration((Date.now() - runSince) / 1000) + ' (observed here)']);
        } else {
          rows.push(['Waiting for', 'not seen from this page']);
        }
      } else {
        targets.forEach(function (target) {
          rows.push([
            'Run #' + target.run.test_id,
            target.checkpoint.identifier + ' · ' +
              target.checkpoint.joined_count + ' of ' +
              target.checkpoint.target_count
          ]);
        });
      }

      var field = el('label', { class: 'field' }, [
        el('span', { class: 'lbl', text: 'Reason sent to the agents' })
      ]);

      var input = el('input', {
        type: 'text',
        value: local.reason,
        placeholder: 'operator_released',
        'data-focus-key': 'release:reason'
      });

      input.addEventListener('input', function () {
        local.reason = input.value;
        refreshDialog();
      });

      field.appendChild(input);

      var body = [impact(rows), field];

      if (reasonTooLong()) {
        body.push(noteStrip('bad', 'A reason may be at most ' +
          MAX_REASON_BYTES + ' bytes; this one is ' +
          byteLength(local.reason.trim()) + '. The server would answer 400.'));
      }

      if (single) {
        body.push(el('p', { class: 'wire', text: wirePreview(single) }));
        body.push(el('p', {
          class: 'wire-note',
          text: 'finished stays false — the server sets it only for reason ' +
            '"complete". start_at is the shared moment every released agent ' +
            'resumes at.'
        }));
      }

      return body;
    }

    function wirePreview(target) {
      var cp = target.checkpoint;

      return 'Each waiting agent receives:\n' +
        '{"command":"wait_checkpoint","content":{\n' +
        '  "identifier":' + JSON.stringify(cp.identifier) + ',\n' +
        '  "finished":false,\n' +
        '  "start_at":<the shared resume moment>,\n' +
        '  "reason":' + JSON.stringify(effectiveReason()) + ',\n' +
        '  "generation":' + cp.generation + ',' +
        '"joined":' + cp.joined_count + ',' +
        '"target":' + cp.target_count + '}}';
    }

    function askDialog() {
      var missing = single
        ? Math.max(0, single.checkpoint.target_count - single.checkpoint.joined_count)
        : 0;

      var title = single
        ? [text('Force-release '), el('span', { class: 'mono', text: single.checkpoint.identifier }), text('?')]
        : [text('Force-release ' + targets.length + ' barriers?')];

      var sub = single
        ? 'Ends round ' + single.checkpoint.generation +
          ' immediately for every agent waiting on the barrier.'
        : 'Ends the current round on every one of them immediately.';

      var foot = [];

      if (single && missing > 0) {
        var warn = noteStrip('', 'The ' + missing + ' missing ' +
          plural(missing, 'agent', 'agents') + ' will fail ' +
          plural(missing, 'its', 'their') + ' next barrier.');
        warn.style.flex = '1 1 auto';
        warn.style.marginRight = '0.4rem';
        foot.push(warn);
      } else {
        foot.push(el('span', { class: 'spacer' }));
      }

      foot.push(button({
        class: 'btn',
        text: 'Cancel',
        key: 'release:cancel',
        onClick: closeDialog
      }));

      foot.push(button({
        class: 'btn primary',
        text: local.busy
          ? 'Releasing…'
          : (single ? 'Release round ' + single.checkpoint.generation
            : 'Release ' + targets.length + ' barriers'),
        key: 'release:go',
        disabled: local.busy || reasonTooLong(),
        onClick: send
      }));

      return {
        icon: 'release',
        title: title,
        sub: sub,
        busy: local.busy,
        body: askBody(),
        foot: foot
      };
    }

    function doneDialog() {
      var total = local.results.reduce(function (sum, item) {
        return sum + item.released;
      }, 0);

      var rows = [['Reason sent', effectiveReason()]];

      if (local.results.length === 1) {
        rows.push(['Released', local.results[0].released + ' ' +
          plural(local.results[0].released, 'agent', 'agents')]);
        rows.push(['Next round', 'generation ' + (local.results[0].generation + 1)]);
      } else {
        rows.push(['Barriers released', String(local.results.length)]);
        rows.push(['Agents released', String(total)]);
      }

      var body = [impact(rows), noteStrip('info',
        'Delivery is not confirmed: the server queues the release on each ' +
        'connection and logs any it cannot write.')];

      if (local.failures.length > 0) {
        body.push(el('div', { class: 'impact' }, local.failures.map(function (failure) {
          return el('div', { class: 'impact-row' }, [
            el('span', { class: 'k', text: failure.label }),
            el('span', { class: 'v', text: failure.message })
          ]);
        })));
      }

      var titleText = local.results.length === 1
        ? 'Round ' + local.results[0].generation + ' released'
        : local.results.length + ' barriers released';

      return {
        icon: 'check',
        iconKind: local.results.length > 0 ? 'ok' : 'bad',
        iconWeight: 2.2,
        title: [text(local.results.length > 0 ? titleText : 'Nothing was released')],
        sub: local.results.length > 0
          ? 'Queued to the ' + total + ' ' + plural(total, 'agent', 'agents') +
            ' that ' + plural(total, 'was', 'were') + ' waiting.'
          : 'Every call failed. Nothing changed on the server.',
        body: body,
        foot: [
          el('span', { class: 'spacer' }),
          button({ class: 'btn primary', text: 'Done', key: 'release:done', onClick: closeDialog })
        ]
      };
    }

    function send() {
      local.busy = true;
      refreshDialog();

      var reason = effectiveReason();

      var chain = targets.reduce(function (previous, target) {
        return previous.then(function () {
          return request(
            'POST',
            API + '/' + encodeURIComponent(target.run.test_id) + '/checkpoints/release',
            {
              accept: 'application/json',
              contentType: 'application/json',
              body: JSON.stringify({
                identifier: target.checkpoint.identifier,
                reason: reason
              })
            }
          ).then(function (result) {
            if (!result.ok) {
              if (result.kind === 'unsupported') {
                markUnsupported('release');
              }

              local.failures.push({
                label: target.checkpoint.identifier,
                message: result.message
              });
              return null;
            }

            return result.response.json().then(function (body) {
              local.results.push({
                identifier: target.checkpoint.identifier,
                released: typeof body.released === 'number' ? body.released : 0,
                generation: typeof body.generation === 'number'
                  ? body.generation
                  : target.checkpoint.generation,
                reason: typeof body.reason === 'string' ? body.reason : reason
              });

              if (String(target.run.test_id) === String(state.route.id)) {
                state.detail.released['cp:' + target.checkpoint.identifier] =
                  local.results[local.results.length - 1];
              }
            }, function () {
              // A 200 with a body this page cannot read still released the
              // round; report it as done with what was asked for.
              local.results.push({
                identifier: target.checkpoint.identifier,
                released: target.checkpoint.joined_count,
                generation: target.checkpoint.generation,
                reason: reason
              });
            });
          });
        });
      }, window.Promise.resolve());

      chain.then(function () {
        local.busy = false;
        local.stage = 'done';
        announce(local.results.length + ' ' +
          plural(local.results.length, 'barrier', 'barriers') + ' released.');
        refreshDialog();
        state.selected = [];
        invalidate();
        poll();
      });
    }

    openDialog(function () {
      return local.stage === 'ask' ? askDialog() : doneDialog();
    });
  }

  /* ---------------- delete flow ---------------- */

  function droppedNote(dropped) {
    if (dropped === null) {
      return '';
    }

    return dropped === 0
      ? ', with no agent attached'
      : ', dropping ' + dropped + ' attached ' +
        plural(dropped, 'agent', 'agents');
  }

  // runDelete issues the deletes and reports what they cost. The server tells
  // us how many live connections it dropped in a response header, which is the
  // only place that number exists after the run is gone.
  function runDelete(runs) {
    var dropped = 0;
    var sawHeader = false;
    var failures = [];

    var chain = runs.reduce(function (previous, run) {
      return previous.then(function () {
        return request('DELETE', API + '/' + encodeURIComponent(run.test_id), {
          accept: 'application/json'
        }).then(function (result) {
          if (!result.ok) {
            if (result.kind === 'unsupported') {
              markUnsupported('deleteRun');
            }

            failures.push({ id: run.test_id, message: result.message });
            return;
          }

          var header = result.response.headers.get('X-TestSync-Connections-Dropped');
          var count = header === null ? NaN : Number(header);

          if (!isNaN(count)) {
            sawHeader = true;
            dropped += count;
          }
        });
      });
    }, window.Promise.resolve());

    return chain.then(function () {
      return {
        dropped: sawHeader ? dropped : null,
        failures: failures,
        deleted: runs.length - failures.length
      };
    });
  }

  function confirmDelete(runs) {
    var local = { stage: 'ask', busy: false, outcome: null };

    var bytes = runs.reduce(function (sum, run) {
      return sum + (run.has_data ? run.data_size_bytes : 0);
    }, 0);
    var attached = runs.reduce(function (sum, run) {
      return sum + run.active_connection_count;
    }, 0);
    var single = runs.length === 1 ? runs[0] : null;

    function askDialog() {
      var rows = single
        ? [
          ['Run', '#' + single.test_id],
          ['Agents attached', single.active_connection_count + ' of ' +
            single.connection_count],
          ['Checkpoints', String(single.checkpoint_count)],
          ['Payload lost', single.has_data ? fmtBytes(single.data_size_bytes) : 'none']
        ]
        : [
          ['Runs', String(runs.length)],
          ['Agents attached', String(attached)],
          ['Payload lost', bytes > 0 ? fmtBytes(bytes) : 'none']
        ];

      var body = [impact(rows)];

      if (attached > 0) {
        body.push(noteStrip('bad', attached + ' ' +
          plural(attached, 'agent is', 'agents are') + ' still attached and ' +
          plural(attached, 'loses', 'lose') + ' the run. Deleting with agents ' +
          'connected is an operator override, not an error.'));
      } else {
        body.push(noteStrip('info',
          'No agent is attached. The run and its stored payload are removed ' +
          'from the registry and from storage.'));
      }

      return {
        icon: 'trash',
        iconKind: 'bad',
        title: [text(single
          ? 'Delete run #' + single.test_id + ' and its payload?'
          : 'Delete ' + runs.length + ' runs and their payloads?')],
        sub: 'This cannot be undone: the server has no restore route.',
        busy: local.busy,
        body: body,
        foot: [
          el('span', { class: 'spacer' }),
          button({ class: 'btn', text: 'Cancel', key: 'delete:cancel', onClick: closeDialog }),
          button({
            class: 'btn danger',
            text: local.busy ? 'Deleting…' : (single
              ? 'Delete run and payload'
              : 'Delete ' + runs.length + ' runs'),
            key: 'delete:go',
            disabled: local.busy,
            onClick: send
          })
        ]
      };
    }

    // payloadRemoved never overstates: the byte total covers every run that
    // was asked for, and a partial failure means some of it is still there.
    function payloadRemoved(outcome) {
      if (outcome.deleted === 0 || bytes === 0) {
        return 'none';
      }

      return outcome.deleted === runs.length
        ? fmtBytes(bytes)
        : 'up to ' + fmtBytes(bytes);
    }

    function doneDialog() {
      var outcome = local.outcome;
      var rows = [
        ['Runs deleted', String(outcome.deleted) + ' of ' + runs.length],
        ['Agents dropped', outcome.dropped === null
          ? 'not reported by this server'
          : String(outcome.dropped)],
        ['Payload removed', payloadRemoved(outcome)]
      ];

      var body = [impact(rows)];

      if (outcome.failures.length > 0) {
        body.push(el('div', { class: 'impact' }, outcome.failures.map(function (failure) {
          return el('div', { class: 'impact-row' }, [
            el('span', { class: 'k', text: 'Run #' + failure.id }),
            el('span', { class: 'v', text: failure.message })
          ]);
        })));
      }

      body.push(noteStrip('info',
        'There is no undo. Re-storing the payload with POST /tests/{testID} ' +
        'would create a different run with none of its checkpoint state.'));

      return {
        icon: 'check',
        iconKind: outcome.deleted > 0 ? 'ok' : 'bad',
        iconWeight: 2.2,
        title: [text(outcome.deleted > 0
          ? (outcome.deleted === 1 ? 'Run deleted' : outcome.deleted + ' runs deleted')
          : 'Nothing was deleted')],
        sub: outcome.deleted > 0
          ? 'The registry entry and the stored payload are gone.'
          : 'Every call failed. Nothing changed on the server.',
        body: body,
        foot: [
          el('span', { class: 'spacer' }),
          button({ class: 'btn primary', text: 'Done', key: 'delete:done', onClick: closeDialog })
        ]
      };
    }

    function send() {
      local.busy = true;
      refreshDialog();

      runDelete(runs).then(function (outcome) {
        local.busy = false;
        local.outcome = outcome;
        local.stage = 'done';
        refreshDialog();

        state.selected = [];
        state.peek = null;

        if (outcome.deleted > 0) {
          var what = single
            ? 'Run #' + single.test_id + ' and its payload were deleted'
            : outcome.deleted + ' runs and their payloads were deleted';

          state.aftermath = {
            kind: 'ok',
            text: what + droppedNote(outcome.dropped) +
              '. Deletion is permanent — there is no undo.'
          };
          announce(what + '.');
        }

        if (outcome.failures.length > 0) {
          state.aftermath = {
            kind: 'bad',
            text: outcome.failures.length + ' of ' + runs.length +
              ' deletions failed: ' + outcome.failures[0].message
          };
        }

        invalidate();
        poll();
      });
    }

    openDialog(function () {
      return local.stage === 'ask' ? askDialog() : doneDialog();
    });
  }

  /* ---------------- disconnect flow ---------------- */

  function confirmDisconnect(body, conn, joined) {
    var local = { stage: 'ask', busy: false, error: null };
    var runID = body.run.test_id;

    function askDialog() {
      var rows = [
        ['Run', '#' + runID],
        ['Agent', '#' + conn.index + ' (conn ' + conn.conn_id + ')'],
        ['Barriers joined', joined.length === 0
          ? 'none'
          : joined.map(function (cp) { return cp.identifier; }).join(', ')]
      ];

      var note = joined.length > 0
        ? noteStrip('', 'Its slot is freed in ' + joined.length + ' waiting ' +
          plural(joined.length, 'barrier', 'barriers') + ', which may complete ' +
          'a round that was blocked on it. The agent itself gets no reply to ' +
          'the barrier it was waiting on.')
        : noteStrip('info', 'The agent is connected but is not waiting at a ' +
          'barrier. Its socket closes with code 1000 and the reason ' +
          '"disconnected by operator".');

      var foot = [
        el('span', { class: 'spacer' }),
        button({ class: 'btn', text: 'Cancel', key: 'kick:cancel', onClick: closeDialog }),
        button({
          class: 'btn danger',
          text: local.busy ? 'Disconnecting…' : 'Disconnect agent',
          key: 'kick:go',
          disabled: local.busy || !supported('disconnect'),
          onClick: send
        })
      ];

      var content = [impact(rows), note];

      if (local.error) {
        content.push(noteStrip('bad', local.error));
      }

      return {
        icon: 'close',
        iconKind: 'bad',
        iconWeight: 2.2,
        title: [text('Disconnect agent #' + conn.index + '?')],
        sub: 'Closes its WebSocket and removes it from run #' + runID + '.',
        busy: local.busy,
        body: content,
        foot: foot
      };
    }

    function doneDialog() {
      return {
        icon: 'check',
        iconKind: 'ok',
        iconWeight: 2.2,
        title: [text('Agent #' + conn.index + ' disconnected')],
        sub: 'Closed with code 1000 and reason "disconnected by operator".',
        body: [noteStrip('info', joined.length > 0
          ? 'Its slot is free in ' + joined.length + ' ' +
            plural(joined.length, 'barrier', 'barriers') + '. A round that was ' +
            'only waiting on this agent completes on the next join.'
          : 'The run keeps its other agents and its stored payload.')],
        foot: [
          el('span', { class: 'spacer' }),
          button({ class: 'btn primary', text: 'Done', key: 'kick:done', onClick: closeDialog })
        ]
      };
    }

    function send() {
      local.busy = true;
      local.error = null;
      refreshDialog();

      request(
        'DELETE',
        API + '/' + encodeURIComponent(runID) + '/connections/' +
          encodeURIComponent(conn.conn_id),
        { accept: 'application/json' }
      ).then(function (result) {
        local.busy = false;

        if (!result.ok) {
          if (result.kind === 'unsupported') {
            markUnsupported('disconnect');
            local.error = 'This server cannot disconnect an agent. ' +
              'The agent is still attached.';
          } else {
            local.error = result.message;
          }

          refreshDialog();
          return;
        }

        local.stage = 'done';
        announce('Agent #' + conn.index + ' disconnected.');
        refreshDialog();
        poll();
      });
    }

    openDialog(function () {
      return local.stage === 'ask' ? askDialog() : doneDialog();
    });
  }

  /* ---------------- payload: read, preview, replace ---------------- */

  // decodePayload turns the stored bytes into something showable. Payloads are
  // arbitrary: anything with control bytes is reported as binary rather than
  // dumped into the page.
  function decodePayload(buffer) {
    var bytes = new Uint8Array(buffer);
    var head = bytes.subarray(0, Math.min(bytes.length, PREVIEW_BYTES));
    var binary = false;

    for (var i = 0; i < head.length; i++) {
      var code = head[i];

      if (code < 0x20 && code !== 0x09 && code !== 0x0a && code !== 0x0d) {
        binary = true;
        break;
      }
    }

    var decoded = '';

    if (!binary) {
      if (window.TextDecoder) {
        decoded = new window.TextDecoder('utf-8').decode(head);
      } else {
        decoded = String.fromCharCode.apply(null, head);
      }
    }

    return {
      text: decoded,
      bytes: bytes.length,
      truncated: bytes.length > head.length,
      binary: binary,
      raw: bytes
    };
  }

  function fetchPayload(runID) {
    return request('GET', API + '/' + encodeURIComponent(runID) + '/data', {
      accept: 'application/octet-stream'
    }).then(function (result) {
      if (!result.ok) {
        if (result.kind === 'unsupported') {
          markUnsupported('readData');
        }

        return { ok: false, message: result.message, reason: result.reason };
      }

      return result.response.arrayBuffer().then(function (buffer) {
        return { ok: true, payload: decodePayload(buffer) };
      }, function () {
        return { ok: false, message: 'The payload could not be read from the response.' };
      });
    });
  }

  function togglePreview(run) {
    if (state.detail.previewOpen) {
      state.detail.previewOpen = false;
      state.detail.previewError = null;
      invalidate();
      return;
    }

    state.detail.previewOpen = true;
    state.detail.previewError = 'Reading the payload…';
    invalidate();

    fetchPayload(run.test_id).then(function (result) {
      if (!result.ok) {
        state.detail.previewError = result.message;
        state.detail.preview = null;
      } else {
        state.detail.previewError = null;
        state.detail.preview = result.payload;
      }

      invalidate();
    });
  }

  // openPayloadDrawer is the View/Replace drawer. It is the one place in the
  // console that shows payload contents, and the one place that writes them.
  function openPayloadDrawer(run) {
    var local = {
      tab: supported('readData') ? 'view' : 'replace',
      stage: 'open',
      loading: supported('readData'),
      loadError: null,
      original: null,     // decoded payload as first read, for Undo
      draft: '',
      confirming: false,
      busy: false,
      error: null,
      written: 0,
      storedBytes: run.has_data ? run.data_size_bytes : 0
    };

    function loadedText() {
      return local.original && !local.original.binary && !local.original.truncated
        ? local.original.text
        : '';
    }

    function viewBody() {
      if (local.loading) {
        return [el('p', { class: 'wire', text: 'Reading the payload…' })];
      }

      if (local.loadError) {
        return [noteStrip('bad', local.loadError)];
      }

      if (!local.original) {
        return [noteStrip('info', 'Nothing is stored for this run yet.')];
      }

      if (local.original.binary) {
        return [noteStrip('', 'This payload is not text: it contains control ' +
          'bytes. ' + fmtBytes(local.original.bytes) + ' stored, not shown.')];
      }

      return [
        el('p', { class: 'wire', text: local.original.text }),
        el('p', {
          class: 'wire-note',
          text: local.original.truncated
            ? 'Showing the first ' + fmtBytes(PREVIEW_BYTES) + ' of ' +
              fmtBytes(local.original.bytes) + '.'
            : fmtBytes(local.original.bytes) + ' in full.'
        }),
        noteStrip('info', 'Agents put arbitrary data here. The run list ' +
          'deliberately shows only its size — this is the one place contents ' +
          'appear.')
      ];
    }

    function replaceBody() {
      var draftBytes = byteLength(local.draft);

      var field = el('label', { class: 'field' }, [
        el('span', { class: 'lbl', text: 'Replacement payload' })
      ]);

      var area = el('textarea', {
        rows: '8',
        'data-focus-key': 'payload:draft',
        spellcheck: 'false'
      });
      area.value = local.draft;
      area.addEventListener('input', function () {
        local.draft = area.value;
        refreshDialog();
      });

      field.appendChild(area);

      var fill = el('span');
      fill.style.width = '100%';

      var meter = el('div', { class: 'meter' }, [
        el('span', { class: 'v', text: 'Draft ' + fmtBytes(draftBytes) }),
        el('div', { class: 'track' }, [fill]),
        el('span', {
          text: 'the server enforces limits.max_data_bytes and answers 413 ' +
            'when a write is over it'
        })
      ]);

      var body = [field, meter];

      if (local.original && local.original.truncated) {
        body.push(noteStrip('bad', 'The stored payload is ' +
          fmtBytes(local.original.bytes) + ', larger than the ' +
          fmtBytes(PREVIEW_BYTES) + ' this page reads. The box above is empty ' +
          'rather than a truncated copy: replacing would throw the rest away.'));
      } else if (local.original && local.original.binary) {
        body.push(noteStrip('bad', 'The stored payload is not text, so it ' +
          'cannot be edited here. Anything written from this box replaces it ' +
          'in full.'));
      }

      body.push(noteStrip('bad', 'Overwrites the payload for every agent in ' +
        'this run, ' + fmtBytes(local.storedBytes) + ' → ' +
        fmtBytes(draftBytes) + '.'));

      if (local.error) {
        body.push(noteStrip('bad', local.error));
      }

      return body;
    }

    function tabs() {
      var bar = el('div', { class: 'tabs' });

      bar.appendChild(button({
        class: 'tab',
        text: 'View',
        pressed: local.tab === 'view' ? 'true' : 'false',
        key: 'drawer:tab-view',
        disabled: !supported('readData'),
        title: supported('readData') ? null : 'This server cannot read a stored payload.',
        onClick: function () {
          local.tab = 'view';
          local.confirming = false;
          refreshDialog();
        }
      }));

      bar.appendChild(button({
        class: 'tab',
        text: 'Replace',
        pressed: local.tab === 'replace' ? 'true' : 'false',
        key: 'drawer:tab-replace',
        disabled: !supported('writeData'),
        title: supported('writeData') ? null : 'This server cannot replace a stored payload.',
        onClick: function () {
          local.tab = 'replace';
          refreshDialog();
        }
      }));

      return bar;
    }

    function openDialogSpec() {
      var foot = [el('span', { class: 'spacer' })];

      if (local.confirming) {
        foot.push(el('span', {
          class: 'muted',
          text: 'Overwrite ' + fmtBytes(local.storedBytes) + ' with ' +
            fmtBytes(byteLength(local.draft)) + '? It is written immediately.'
        }));
        foot.push(button({
          class: 'btn',
          text: 'Cancel',
          key: 'drawer:cancel-replace',
          onClick: function () {
            local.confirming = false;
            refreshDialog();
          }
        }));
        foot.push(button({
          class: 'btn danger',
          text: local.busy ? 'Writing…' : 'Replace now',
          key: 'drawer:confirm-replace',
          disabled: local.busy,
          onClick: send
        }));
      } else {
        foot.push(button({
          class: 'btn',
          text: 'Close',
          key: 'drawer:close',
          onClick: closeDialog
        }));

        if (local.tab === 'replace') {
          foot.push(button({
            class: 'btn danger',
            text: 'Replace payload',
            key: 'drawer:replace',
            disabled: !supported('writeData'),
            onClick: function () {
              local.confirming = true;
              refreshDialog();
            }
          }));
        }
      }

      return {
        icon: 'eye',
        iconKind: 'info',
        iconWeight: 1.7,
        title: [text('Payload for run '), el('span', { class: 'mono', text: '#' + run.test_id })],
        sub: (local.storedBytes > 0 ? fmtBytes(local.storedBytes) + ' stored' : 'nothing stored') +
          ' · the server does not report when it was written',
        beforeBody: [tabs()],
        busy: local.busy,
        body: local.tab === 'view' ? viewBody() : replaceBody(),
        foot: foot
      };
    }

    function savedDialogSpec() {
      var canUndo = local.original !== null && !local.original.truncated;
      var undoNote = local.original === null
        ? 'There was no payload here before, so there is nothing to put back. ' +
          'Deleting a stored payload is not something this API offers.'
        : 'The previous payload was ' + fmtBytes(local.original.bytes) +
          ', larger than the ' + fmtBytes(PREVIEW_BYTES) + ' this page read, ' +
          'so it cannot be put back in full. It is gone.';

      return {
        icon: 'check',
        iconKind: 'ok',
        iconWeight: 2.2,
        title: [text('Payload replaced')],
        sub: fmtBytes(local.written) + ' written to run #' + run.test_id +
          ' a moment ago.',
        busy: local.busy,
        body: [
          noteStrip('info', 'Agents that already read the old payload will not ' +
            'see this. Only reads after now return it.'),
          canUndo ? null : noteStrip('', undoNote)
        ].filter(Boolean),
        foot: [
          el('span', { class: 'spacer' }),
          button({
            class: 'btn',
            text: local.busy ? 'Restoring…' : 'Undo',
            key: 'drawer:undo',
            disabled: !canUndo || local.busy,
            title: canUndo
              ? 'Write the previous payload back'
              : 'The previous payload is not available to restore.',
            onClick: undo
          }),
          button({ class: 'btn primary', text: 'Done', key: 'drawer:done', onClick: closeDialog })
        ]
      };
    }

    function put(body, bytes, onDone) {
      local.busy = true;
      local.error = null;
      refreshDialog();

      request('PUT', TESTS + '/' + encodeURIComponent(run.test_id), {
        accept: 'application/json',
        contentType: 'application/octet-stream',
        body: body
      }).then(function (result) {
        local.busy = false;

        if (!result.ok) {
          if (result.kind === 'unsupported') {
            markUnsupported('writeData');
            local.error = 'This server cannot replace a stored payload. ' +
              'Nothing was written and the draft above is untouched.';
          } else {
            local.error = result.message;
          }

          local.confirming = false;
          local.stage = 'open';
          local.tab = 'replace';
          refreshDialog();
          return;
        }

        local.storedBytes = bytes;
        onDone();
        refreshDialog();
        poll();
      });
    }

    function send() {
      var bytes = byteLength(local.draft);

      put(local.draft, bytes, function () {
        local.written = bytes;
        local.confirming = false;
        local.stage = 'saved';
        announce('Payload for run #' + run.test_id + ' replaced with ' +
          fmtBytes(bytes) + '.');
      });
    }

    function undo() {
      if (!local.original) {
        return;
      }

      // The original goes back as the exact bytes that were read, not as a
      // re-encoded string: a payload is not necessarily text.
      var blob = new window.Blob([local.original.raw]);

      put(blob, local.original.bytes, function () {
        local.stage = 'open';
        local.tab = 'view';
        announce('Previous payload restored.');
      });
    }

    openDialog(function () {
      return local.stage === 'open' ? openDialogSpec() : savedDialogSpec();
    }, true);

    if (supported('readData')) {
      fetchPayload(run.test_id).then(function (result) {
        local.loading = false;

        if (!result.ok) {
          local.loadError = result.reason === 'data_not_found' ? null : result.message;
          local.original = null;

          if (!supported('readData')) {
            local.tab = 'replace';
          }
        } else {
          local.original = result.payload;
          local.storedBytes = result.payload.bytes;
          local.draft = loadedText();
        }

        refreshDialog();
      });
    }
  }

  /* ---------------- wiring ---------------- */

  function init() {
    dom.topbar = document.querySelector('.topbar');
    dom.main = document.getElementById('main');
    dom.footer = document.querySelector('.footer');
    dom.listView = document.getElementById('list-view');
    dom.listBody = document.getElementById('list-body');
    dom.listSummary = document.getElementById('list-summary');
    dom.toolbar = document.getElementById('list-toolbar');
    dom.detailView = document.getElementById('detail-view');
    dom.detailBody = document.getElementById('detail-body');
    dom.detailHeading = document.getElementById('detail-heading');
    dom.detailSummary = document.getElementById('detail-summary');
    dom.banner = document.getElementById('banner');
    dom.capabilityNote = document.getElementById('capability-note');
    dom.feed = document.getElementById('feed-status');
    dom.feedText = document.getElementById('feed-text');
    dom.dialogRoot = document.getElementById('dialog-root');
    dom.actionStatus = document.getElementById('action-status');

    dom.chips = {
      all: document.getElementById('chip-all'),
      waiting: document.getElementById('chip-waiting'),
      idle: document.getElementById('chip-idle'),
      data: document.getElementById('chip-data')
    };

    Object.keys(dom.chips).forEach(function (name) {
      dom.chips[name].addEventListener('click', function () {
        state.filter = name;
        invalidate();
      });
    });

    var search = document.getElementById('query');
    search.addEventListener('input', function () {
      state.query = search.value;
      invalidate();
    });

    var intervalSelect = document.getElementById('interval');
    state.intervalMs = Number(intervalSelect.value) || 2000;

    intervalSelect.addEventListener('change', function () {
      state.intervalMs = Number(intervalSelect.value) || 2000;
      renderStatus();
      renderBanner();
      schedule();
    });

    var pauseButton = document.getElementById('pause');
    pauseButton.addEventListener('click', function () {
      state.paused = !state.paused;
      pauseButton.setAttribute('aria-pressed', String(state.paused));
      pauseButton.textContent = state.paused ? 'Resume' : 'Pause';
      renderStatus();
      renderBanner();

      if (state.paused) {
        schedule();
      } else {
        poll();
      }
    });

    document.getElementById('refresh').addEventListener('click', poll);
    window.addEventListener('hashchange', onRouteChange);

    state.route = parseRoute();
    dom.listView.hidden = state.route.name !== 'list';
    dom.detailView.hidden = state.route.name !== 'detail';
    dom.toolbar.hidden = state.route.name !== 'list';

    window.setInterval(tickLiveNodes, 1000);
    poll();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
}());
