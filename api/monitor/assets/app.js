/*
 * TestSync monitor. Vanilla JS, no build step, no dependencies.
 *
 * Everything the server sends is treated as untrusted text: nodes are built
 * with createElement and filled with textContent, and no HTML-parsing sink is
 * ever handed a server string. Checkpoint identifiers come from the agents and
 * will contain hostile input eventually.
 */

(function () {
  'use strict';

  var API = '/api/v1/runs';

  var state = {
    route: { name: 'list', id: null },
    intervalMs: 2000,
    paused: false,
    inFlight: false,
    lastSuccess: null,   // Date of the last good response
    error: null,         // string shown in the banner
    payload: null,       // last decoded body for the current route
    signature: null,     // JSON of the last rendered payload
    waitingSince: {}     // key -> ms timestamp of the first waiting sighting
  };

  // liveNodes are re-stamped every second so that durations keep counting
  // without a re-render, which would otherwise steal keyboard focus.
  var liveNodes = [];
  var timer = null;

  var dom = {};

  /* ---------------- small helpers ---------------- */

  function el(tag, props, children) {
    var node = document.createElement(tag);

    if (props) {
      Object.keys(props).forEach(function (key) {
        if (key === 'text') {
          node.textContent = props[key];
        } else if (key === 'class') {
          node.className = props[key];
        } else {
          node.setAttribute(key, props[key]);
        }
      });
    }

    (children || []).forEach(function (child) {
      node.appendChild(child);
    });

    return node;
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

    dom.listView.hidden = next.name !== 'list';
    dom.detailView.hidden = next.name !== 'detail';

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
      render();
    }).catch(function (err) {
      // A transport failure arrives as a TypeError whose message ("Failed to
      // fetch") is browser jargon, so only our own messages are shown.
      state.error = err && err.explained
        ? err.message
        : 'Cannot reach the server.';
      renderStatus();
      renderBanner();
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

  /* ---------------- chrome ---------------- */

  function renderStatus() {
    var cls = 'feed';
    var text;

    if (state.error) {
      cls += ' error';
      text = 'No contact with server';
    } else if (state.paused) {
      cls += ' paused';
      text = 'Paused';
    } else {
      cls += ' live';
      text = 'Live';
    }

    dom.feed.className = cls;

    if (state.lastSuccess) {
      var suffix = document.createTextNode('');
      clear(dom.feedText);
      dom.feedText.appendChild(document.createTextNode(text + ' · updated '));
      dom.feedText.appendChild(suffix);
      live(suffix, state.lastSuccess.getTime(), function (secs) {
        return fmtDuration(secs) + ' ago';
      });
    } else {
      dom.feedText.textContent = text;
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

  function tick() {
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

  function render() {
    var signature = JSON.stringify(state.payload);

    renderBanner();

    if (signature === state.signature) {
      renderStatus();
      return;
    }

    state.signature = signature;
    liveNodes = [];

    if (state.route.name === 'detail') {
      renderDetail(state.payload);
    } else {
      renderList(state.payload);
    }

    renderStatus();
  }

  function renderList(body) {
    var runs = (body && body.runs) || [];
    var waiting = runs.filter(function (run) { return run.waiting; }).length;

    dom.listSummary.textContent = runs.length === 0
      ? 'Nothing running'
      : runs.length + ' ' + plural(runs.length, 'run', 'runs') + ' · ' +
        waiting + ' waiting on a checkpoint';

    clear(dom.listBody);

    if (runs.length === 0) {
      dom.listBody.appendChild(emptyPanel(
        'No test runs yet',
        'A run appears as soon as an agent connects to /register/{testID} ' +
        'or test data is stored with POST /tests/{testID}.'
      ));
      return;
    }

    var head = el('tr', null, [
      el('th', { text: 'Run', scope: 'col' }),
      el('th', { text: 'Checkpoints', scope: 'col' }),
      el('th', { text: 'Agents', scope: 'col' }),
      el('th', { text: 'Data', scope: 'col' }),
      el('th', { text: 'Age', scope: 'col' })
    ]);

    var tbody = el('tbody', null, runs.map(runRow));

    var table = el('table', null, [
      el('caption', {
        class: 'sr-only',
        text: 'Test runs known to this server, newest state on every refresh'
      }),
      el('thead', null, [head]),
      tbody
    ]);

    var wrap = el('div', { class: 'panel table-wrap' }, [table]);
    dom.listBody.appendChild(wrap);
  }

  function runRow(run) {
    var link = el('a', {
      class: 'run-link',
      href: '#/run/' + run.test_id
    }, [document.createTextNode('#' + run.test_id)]);

    var status = run.waiting
      ? badge('wait', 'WAITING')
      : (run.checkpoint_count > 0
        ? badge('ok', 'ALL CLEAR')
        : badge('idle', 'NO CHECKPOINTS'));

    var checkpointCell = el('td', null, [status]);

    if (run.waiting) {
      var detail = el('div', { class: 'cell-note' });
      detail.appendChild(document.createTextNode(
        run.waiting_checkpoint_count + ' of ' + run.checkpoint_count + ' ' +
        plural(run.checkpoint_count, 'checkpoint', 'checkpoints') + ' blocked'
      ));

      var since = state.waitingSince['run:' + run.test_id];
      if (since) {
        detail.appendChild(document.createTextNode(' · seen waiting '));
        detail.appendChild(live(document.createTextNode(''), since, fmtDuration));
      }

      checkpointCell.appendChild(detail);
    }

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

    var age = el('td', { class: 'muted' });
    age.appendChild(live(el('span'), startedAt(run), fmtDuration));

    var row = el('tr', run.waiting ? { class: 'is-waiting' } : null, [
      el('td', null, [link]),
      checkpointCell,
      agents,
      el('td', {
        class: run.has_data ? '' : 'muted',
        text: run.has_data ? fmtBytes(run.data_size_bytes) : 'none'
      }),
      age
    ]);

    return row;
  }

  function renderDetail(body) {
    var run = body && body.run;

    clear(dom.detailBody);

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
      metaCell('Checkpoints', el('dd', {
        text: run.waiting_checkpoint_count + ' waiting of ' + run.checkpoint_count
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
        text: waiting.length + ' waiting · ' + idle.length + ' idle'
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

    var item = el('li', {
      class: 'panel checkpoint ' + (cp.waiting ? 'waiting' : 'idle')
    });

    item.appendChild(el('div', { class: 'checkpoint-head' }, [
      el('span', { class: 'cp-name', text: cp.identifier }),
      cp.waiting ? badge('wait', 'WAITING') : badge('ok', 'RELEASED')
    ]));

    if (cp.waiting && cp.generation > 1) {
      item.appendChild(el('p', {
        class: 'cp-note',
        text: 'Round ' + cp.generation + '.'
      }));
    }

    item.appendChild(el('p', {
      class: 'cp-counts',
      text: joined + ' of ' + target + ' ' + plural(target, 'agent', 'agents') +
        ' joined'
    }));

    var fill = el('span');
    fill.style.width = percent + '%';

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
        note.appendChild(document.createTextNode(lead + ', seen waiting '));
        note.appendChild(live(document.createTextNode(''), since, fmtDuration));
      } else {
        note.textContent = lead + '.';
      }
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

  function agentSection(body) {
    var connections = body.connections || [];
    var joined = {};

    (body.checkpoints || []).forEach(function (cp) {
      if (!cp.waiting) {
        return;
      }

      (cp.members || []).forEach(function (idx) {
        joined[idx] = true;
      });
    });

    var section = el('section', { 'aria-labelledby': 'agents-heading' });

    section.appendChild(el('div', { class: 'subhead' }, [
      el('h2', { id: 'agents-heading', text: 'Agents' }),
      el('p', {
        class: 'section-note',
        text: body.run.active_connection_count + ' of ' +
          connections.length + ' still connected'
      })
    ]));

    if (connections.length === 0) {
      section.appendChild(emptyPanel(
        'No agents connected',
        'This run exists but nothing is registered on ' +
        '/register/' + body.run.test_id + ' right now.'
      ));
      return section;
    }

    var list = el('ul', { class: 'panel agents' });

    connections.forEach(function (conn) {
      var cls = 'agent' + (conn.active ? '' : ' closed') +
        (joined[conn.index] ? ' joined' : '');

      var item = el('li', {
        class: cls,
        title: joined[conn.index]
          ? 'Waiting at a checkpoint'
          : (conn.active ? 'Connected' : 'Disconnected')
      }, [
        el('span', { class: 'dot', 'aria-hidden': 'true' }),
        el('span', {
          text: '#' + conn.index + (conn.active ? '' : ' closed') +
            (joined[conn.index] ? ' · waiting' : '')
        })
      ]);

      list.appendChild(item);
    });

    section.appendChild(list);
    return section;
  }

  function emptyPanel(title, detail) {
    return el('div', { class: 'panel empty' }, [
      el('strong', { text: title }),
      el('span', { text: detail })
    ]);
  }

  /* ---------------- wiring ---------------- */

  function init() {
    dom.listView = document.getElementById('list-view');
    dom.listBody = document.getElementById('list-body');
    dom.listSummary = document.getElementById('list-summary');
    dom.detailView = document.getElementById('detail-view');
    dom.detailBody = document.getElementById('detail-body');
    dom.detailHeading = document.getElementById('detail-heading');
    dom.detailSummary = document.getElementById('detail-summary');
    dom.banner = document.getElementById('banner');
    dom.feed = document.getElementById('feed-status');
    dom.feedText = document.getElementById('feed-text');

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

    window.setInterval(tick, 1000);
    poll();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
}());
