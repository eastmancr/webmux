import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { appendFile, chmod, cp, mkdtemp, mkdir, rm } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'webmux-smoke-'));
const binary = path.join(tempRoot, 'webmux-dev');
const profile = path.join(tempRoot, 'chromium');
let server;
let chromium;
let serverLog = '';
let chromiumLog = '';

function reservePort() {
    return new Promise((resolve, reject) => {
        const listener = net.createServer();
        listener.unref();
        listener.once('error', reject);
        listener.listen(0, '127.0.0.1', () => {
            const { port } = listener.address();
            listener.close(error => error ? reject(error) : resolve(port));
        });
    });
}

function wait(milliseconds) {
    return new Promise(resolve => setTimeout(resolve, milliseconds));
}

async function waitFor(check, description, timeout = 15000) {
    const deadline = Date.now() + timeout;
    let lastError;
    while (Date.now() < deadline) {
        try {
            const result = await check();
            if (result) return result;
        } catch (error) {
            lastError = error;
        }
        await wait(100);
    }
    throw new Error(`Timed out waiting for ${description}${lastError ? `: ${lastError.message}` : ''}`);
}

async function stopProcess(child, signal, timeout = 10000) {
    if (!child || child.exitCode !== null) return;
    const exited = new Promise(resolve => child.once('exit', resolve));
    child.kill(signal);
    if (await Promise.race([exited.then(() => true), wait(timeout).then(() => false)])) return;
    child.kill('SIGKILL');
    await exited;
}

function findChromium() {
    const candidates = [process.env.CHROMIUM, 'chromium', 'chromium-browser', 'google-chrome'].filter(Boolean);
    for (const candidate of candidates) {
        const result = spawnSync(candidate, ['--version'], { stdio: 'ignore' });
        if (result.status === 0) return candidate;
    }
    throw new Error('Chromium not found; set CHROMIUM to its executable path');
}

class DevTools {
    constructor(url) {
        this.socket = new WebSocket(url);
        this.pending = new Map();
        this.nextID = 1;
        this.exceptions = [];
        this.consoleErrors = [];
        this.scriptFailures = [];
        this.socket.addEventListener('message', event => this.handleMessage(JSON.parse(event.data)));
    }

    async open() {
        await new Promise((resolve, reject) => {
            this.socket.addEventListener('open', resolve, { once: true });
            this.socket.addEventListener('error', reject, { once: true });
        });
        await this.command('Runtime.enable');
        await this.command('Page.enable');
        await this.command('Network.enable');
    }

    handleMessage(message) {
        if (message.id) {
            const pending = this.pending.get(message.id);
            this.pending.delete(message.id);
            if (message.error) pending.reject(new Error(message.error.message));
            else pending.resolve(message.result);
            return;
        }
        if (message.method === 'Runtime.exceptionThrown') {
            this.exceptions.push(message.params.exceptionDetails.text);
        }
        if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') {
            this.consoleErrors.push(message.params.args.map(arg => arg.value || arg.description || '').join(' '));
        }
        if (message.method === 'Network.responseReceived' && message.params.type === 'Script' && message.params.response.status >= 400) {
            this.scriptFailures.push(`${message.params.response.status} ${message.params.response.url}`);
        }
        if (message.method === 'Network.loadingFailed' && message.params.type === 'Script') {
            this.scriptFailures.push(`${message.params.errorText} ${message.params.requestId}`);
        }
    }

    command(method, params = {}) {
        const id = this.nextID++;
        this.socket.send(JSON.stringify({ id, method, params }));
        return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
    }

    async evaluate(expression) {
        const result = await this.command('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
        return result.result.value;
    }

    clearErrors() {
        this.exceptions.length = 0;
        this.consoleErrors.length = 0;
        this.scriptFailures.length = 0;
    }

    close() {
        this.socket.close();
    }
}

try {
    for (const name of ['bin', 'config', 'data', 'state', 'uploads']) {
        await mkdir(path.join(tempRoot, name));
    }
    const fakeOpenCode = path.join(tempRoot, 'bin', 'opencode');
    await cp(path.join(root, 'scripts', 'testdata', 'fake-opencode.mjs'), fakeOpenCode);
    await chmod(fakeOpenCode, 0o755);
    const staticDir = path.join(tempRoot, 'static');
    await cp(path.join(root, 'static'), staticDir, { recursive: true });

    const build = spawnSync('go', ['build', '-tags', 'dev', '-o', binary, '.'], { cwd: root, stdio: 'inherit' });
    assert.equal(build.status, 0, 'development build failed');

    const [serverPort, panePort, debugPort] = await Promise.all([reservePort(), reservePort(), reservePort()]);
    const baseURL = `http://127.0.0.1:${serverPort}`;
    const mountedURL = `${baseURL}/webmux/`;
    const isolatedEnv = {
        ...process.env,
        HISTFILE: '/dev/null',
        WEBMUX_STATIC_DIR: staticDir,
        XDG_CONFIG_HOME: path.join(tempRoot, 'config'),
        XDG_DATA_HOME: path.join(tempRoot, 'data'),
        XDG_STATE_HOME: path.join(tempRoot, 'state'),
        PATH: `${path.join(tempRoot, 'bin')}:${process.env.PATH}`,
    };
    const serverArgs = [
        '-port', String(serverPort),
        '-pane-port-start', String(panePort),
        '-upload-dir', path.join(tempRoot, 'uploads'),
        root,
    ];
    const startServer = async () => {
        server = spawn(binary, serverArgs, { cwd: root, env: isolatedEnv, stdio: ['ignore', 'pipe', 'pipe'] });
        server.stdout.on('data', chunk => { serverLog += chunk; });
        server.stderr.on('data', chunk => { serverLog += chunk; });
        return waitFor(async () => {
            const response = await fetch(`${baseURL}/api/info`);
            return response.ok ? response.json() : null;
        }, 'isolated webmux server');
    };
    let serverInfo = await startServer();

    const paneResponse = await fetch(`${mountedURL}api/panes`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: 'terminal', name: 'browser-smoke-test' }),
    });
    const paneBody = await paneResponse.text();
    assert.equal(paneResponse.status, 201, paneBody);
    const pane = JSON.parse(paneBody);
    const openCodeResponse = await fetch(`${mountedURL}api/panes`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: 'opencode', name: 'opencode-attention-test' }),
    });
    const openCodeBody = await openCodeResponse.text();
    assert.equal(openCodeResponse.status, 201, openCodeBody);
    const openCodePane = JSON.parse(openCodeBody);

    chromium = spawn(findChromium(), [
        '--headless=new',
        '--no-sandbox',
        '--disable-gpu',
        '--disable-extensions',
        `--remote-debugging-port=${debugPort}`,
        `--user-data-dir=${profile}`,
        'about:blank',
    ], { stdio: ['ignore', 'pipe', 'pipe'] });

    chromium.stdout.on('data', chunk => { chromiumLog += chunk; });
    chromium.stderr.on('data', chunk => { chromiumLog += chunk; });
    const target = await waitFor(async () => {
        const targets = await fetch(`http://127.0.0.1:${debugPort}/json/list`).then(response => response.json());
        return targets.find(candidate => candidate.type === 'page');
    }, 'Chromium DevTools target');
    const tools = new DevTools(target.webSocketDebuggerUrl);
    await tools.open();

    await tools.command('Page.navigate', { url: mountedURL });
    await waitFor(() => tools.evaluate('window.app?.terminals.size === 1'), 'embedded terminal');
    const attentionSoundBehavior = await tools.evaluate(`(() => {
        const terminalPane = Array.from(window.app.panes.values()).find(pane => pane.type === 'terminal');
        const openCodePane = Array.from(window.app.panes.values()).find(pane => pane.type === 'opencode');
        let plays = 0;
        const originalPlay = window.app.playAttentionSound;
        window.app.playAttentionSound = () => plays++;
        window.app.clearPaneAttention(terminalPane.id);
        window.app.clearPaneAttention(openCodePane.id);
        window.app.markPaneAttention(terminalPane.id, true);
        window.app.markPaneAttention(terminalPane.id, true);
        window.app.markPaneAttention(openCodePane.id, true);
        const enabledPlays = plays;
        window.app.clearPaneAttention(terminalPane.id);
        window.app.settings.panes.playAttentionSound = false;
        window.app.markPaneAttention(terminalPane.id, true);
        const disabledPlays = plays;
        window.app.settings.panes.playAttentionSound = true;
        window.app.clearPaneAttention(terminalPane.id);
        window.app.clearPaneAttention(openCodePane.id);
        window.app.playAttentionSound = originalPlay;
        return { enabledPlays, disabledPlays };
    })()`);
    assert.deepEqual(attentionSoundBehavior, { enabledPlays: 1, disabledPlays: 1 }, 'attention sound should play once for new terminal attention only');
    const attentionSoundPreview = await tools.evaluate(`(async () => {
        let previewed = 0;
        const originalPlay = window.app.playAttentionSound;
        window.app.playAttentionSound = preview => { if (preview) previewed++; };
        document.getElementById('preview-attention-sound').click();
        await new Promise(resolve => setTimeout(resolve, 0));
        window.app.playAttentionSound = originalPlay;
        return previewed;
    })()`);
    assert.equal(attentionSoundPreview, 1, 'attention sound preview should use the normal playback path');
    await tools.evaluate(`(() => {
        const group = Array.from(window.app.groups.values()).find(candidate => candidate.paneIds.includes('${openCodePane.id}'));
        window.app.activateGroup(group.id, '${openCodePane.id}');
    })()`);
    await waitFor(() => tools.evaluate(`window.app?.sharedIframes.get('opencode')?.contentWindow?.__fakeOpenCodeReady`), 'fake OpenCode');
    assert.deepEqual(tools.exceptions, [], `browser exceptions: ${tools.exceptions.join('; ')}`);
    assert.deepEqual(tools.consoleErrors, [], `console errors: ${tools.consoleErrors.join('; ')}`);
    assert.deepEqual(tools.scriptFailures, [], `script load failures: ${tools.scriptFailures.join('; ')}`);

    const fakeOpenCodeURL = `http://127.0.0.1:${openCodePane.port}`;
    await tools.evaluate(`window.__attentionSoundCount = 0; window.app.playAttentionSound = () => { window.__attentionSoundCount++; }`);
    await fetch(`${fakeOpenCodeURL}/test/question/ask?id=focused-question`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), 'focused interactive attention');
    assert.equal(await tools.evaluate(`window.__attentionSoundCount`), 1, 'question attention should play a sound');
    await fetch(`${fakeOpenCodeURL}/question/focused-question/reply`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'focused interactive resolution');
    await fetch(`${fakeOpenCodeURL}/test/permission/ask?id=focused-permission`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), 'focused permission attention');
    assert.equal(await tools.evaluate(`window.__attentionSoundCount`), 2, 'permission attention should play a sound');
    await fetch(`${fakeOpenCodeURL}/permission/focused-permission/reject`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'focused permission rejection');

    await tools.evaluate(`(() => {
        const group = Array.from(window.app.groups.values()).find(candidate => candidate.paneIds.includes('${pane.id}'));
        window.app.activateGroup(group.id, '${pane.id}');
    })()`);
    await tools.evaluate(`window.app.saveUIState()`);
    await fetch(`${fakeOpenCodeURL}/test/question/ask?id=question-one` , { method: 'POST' });
    await waitFor(() => tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}') && document.querySelector('[data-pane-id="${openCodePane.id}"]')?.classList.contains('has-attention')`), 'question attention');
    await fetch(`${fakeOpenCodeURL}/test/permission/ask?id=permission-one`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`window.__attentionSoundCount === 4`), 'sound for multiple interactive attention events');
    await fetch(`${fakeOpenCodeURL}/question/question-one/reply`, { method: 'POST' });
    assert.equal(await tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), true, 'resolving one of multiple causes should retain attention');
    await fetch(`${fakeOpenCodeURL}/permission/permission-one/reply`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'resolved interactive attention');

    await tools.evaluate(`(() => {
        const iframe = window.app.sharedIframes.get('opencode');
        iframe.contentWindow.localStorage.setItem('opencode.window.browser.dat:tabs', JSON.stringify([
            { type: 'session', server: 'webmux', sessionId: 'background-session' },
        ]));
        iframe.contentWindow.localStorage.setItem('opencode.global.dat:notification', JSON.stringify({
            list: [{ type: 'turn-complete', session: 'background-session', directory: '/tmp/webmux-attention', time: 1, viewed: false }],
        }));
    })()`);
    await waitFor(() => tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), 'unviewed notification attention');
    assert.equal(await tools.evaluate(`window.__attentionSoundCount`), 4, 'natively audible notification should not duplicate its sound');
    await tools.evaluate(`window.app.sharedIframes.get('opencode').contentWindow.localStorage.setItem('opencode.window.browser.dat:tabs', '[]')`);
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'closed notification tab resolution');
    await tools.evaluate(`(() => {
        const iframe = window.app.sharedIframes.get('opencode');
        iframe.contentWindow.localStorage.setItem('opencode.window.browser.dat:tabs', JSON.stringify([
            { type: 'session', server: 'webmux', sessionId: 'background-session' },
        ]));
        iframe.contentWindow.localStorage.setItem('opencode.global.dat:notification', JSON.stringify({
            list: [{ type: 'turn-complete', session: 'background-session', directory: '/tmp/webmux-attention', time: 1, viewed: true }],
        }));
    })()`);
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'viewed notification resolution');

    await fetch(`${fakeOpenCodeURL}/test/question/ask?id=restart-question`, { method: 'POST' });
    await waitFor(() => tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), 'pre-restart question attention');
    await waitFor(async () => {
        const storage = await fetch(`${baseURL}/api/storage/opencode`).then(response => response.json());
        return Boolean(storage.items?.['webmux.internal.opencode.attention']);
    }, 'persisted OpenCode attention causes');

    const initialRunId = serverInfo.serverRunId;
    const initialAssetVersion = serverInfo.assetVersion;
    await tools.evaluate(`window.__webmuxSmokeDocumentToken = 'same-document'; window.app.openLogsModal()`);
    await waitFor(() => tools.evaluate(`Boolean(window.app.logsRefreshInterval?.close)`), 'logs event stream');
    await stopProcess(server, 'SIGTERM');
    await waitFor(() => tools.evaluate(`window.app?.connectionMode === 'recovering' && window.app.terminals.size === 0`), 'client connection pause');
    serverInfo = await startServer();
    assert.notEqual(serverInfo.serverRunId, initialRunId, 'replacement server should have a new run ID');
    assert.equal(serverInfo.assetVersion, initialAssetVersion, 'unchanged static assets should retain their version');
    await waitFor(() => tools.evaluate(`window.app?.serverRunId === '${serverInfo.serverRunId}' && window.app.connectionMode === 'active' && window.app.serverConnected && window.app.terminals.size === 1 && !window.app.serverRecoveryPromise`), 'in-place server recovery');
    await tools.evaluate(`(() => {
        const group = Array.from(window.app.groups.values()).find(candidate => candidate.paneIds.includes('${openCodePane.id}'));
        window.app.activateGroup(group.id, '${openCodePane.id}');
    })()`);
    await waitFor(() => tools.evaluate(`window.app.sharedIframes.get('opencode')?.contentWindow?.__fakeOpenCodeReady`), 'recovered fake OpenCode');
    assert.equal(await tools.evaluate(`window.app.attentionPaneIds.has('${openCodePane.id}')`), true, 'OpenCode attention should survive restart source rehydration');
    await tools.evaluate(`(() => {
        const frame = window.app.sharedIframes.get('opencode').contentWindow;
        frame.__fakeOpenCodeEvents.close();
        return frame.fetch('/question/restart-question/reply', { method: 'POST' });
    })()`);
    await waitFor(() => tools.evaluate(`!window.app.attentionPaneIds.has('${openCodePane.id}')`), 'post-restart question resolution');
    await tools.evaluate(`(() => {
        const group = Array.from(window.app.groups.values()).find(candidate => candidate.paneIds.includes('${pane.id}'));
        window.app.activateGroup(group.id, '${pane.id}');
    })()`);
    await tools.evaluate(`window.app.saveUIState()`);
    const recoveredState = await tools.evaluate(`({
        token: window.__webmuxSmokeDocumentToken,
        modal: Boolean(document.getElementById('server-restart-modal')),
        paneSocket: window.app.paneSocket?.readyState,
        clipboardSocket: window.app.clipboardSocket?.readyState,
        scratchEvents: window.app.scratchEventSource?.readyState,
        markedEvents: window.app.markedEventSource?.readyState,
        logsEvents: Boolean(window.app.logsRefreshInterval?.close),
    })`);
    assert.equal(recoveredState.token, 'same-document', 'compatible restart should preserve the document');
    assert.equal(recoveredState.modal, false, 'compatible restart should not show the refresh modal');
    assert.equal(recoveredState.paneSocket, 1, 'pane events should reconnect');
    assert.ok(recoveredState.clipboardSocket === 0 || recoveredState.clipboardSocket === 1, 'clipboard events should reconnect');
    assert.ok(recoveredState.scratchEvents === 0 || recoveredState.scratchEvents === 1, 'scratch events should reconnect');
    assert.ok(recoveredState.markedEvents === 0 || recoveredState.markedEvents === 1, 'marked events should reconnect');
    assert.equal(recoveredState.logsEvents, true, 'logs events should reconnect');

    tools.clearErrors();
    await tools.evaluate(`window.__webmuxSmokeDocumentToken = 'stale-document'`);
    await stopProcess(server, 'SIGTERM');
    await waitFor(() => tools.evaluate(`window.app?.connectionMode === 'recovering'`), 'second client connection pause');
    await appendFile(path.join(staticDir, 'app.js'), `\nwindow.__webmuxSmokeChangedAsset = true;\n`);
    const compatibleAssetVersion = serverInfo.assetVersion;
    serverInfo = await startServer();
    assert.notEqual(serverInfo.assetVersion, compatibleAssetVersion, 'changed app.js should change the asset version');
    await waitFor(() => tools.evaluate(`window.app?.connectionMode === 'refresh-required' && Boolean(document.getElementById('server-restart-modal'))`), 'refresh-required restart modal');
    const stoppedState = await tools.evaluate(`({
        token: window.__webmuxSmokeDocumentToken,
        changedAssetLoaded: Boolean(window.__webmuxSmokeChangedAsset),
        paneSocket: window.app.paneSocket,
        clipboardSocket: window.app.clipboardSocket,
        scratchEvents: window.app.scratchEventSource,
        markedEvents: window.app.markedEventSource,
        devReloadSocket: window.app.devReloadSocket,
        logsEvents: window.app.logsRefreshInterval,
        terminals: window.app.terminals.size,
        sharedIframes: window.app.sharedIframes.size,
        scratchTimer: window.app.scratchReconnectTimer,
        markedTimer: window.app.markedReconnectTimer,
        clipboardTimer: window.app.clipboardReconnectTimer,
        devReloadTimer: window.app.devReloadReconnectTimer,
    })`);
    assert.deepEqual(stoppedState, {
        token: 'stale-document',
        changedAssetLoaded: false,
        paneSocket: null,
        clipboardSocket: null,
        scratchEvents: null,
        markedEvents: null,
        devReloadSocket: null,
        logsEvents: null,
        terminals: 0,
        sharedIframes: 0,
        scratchTimer: null,
        markedTimer: null,
        clipboardTimer: null,
        devReloadTimer: null,
    }, 'refresh modal should leave no reconnecting application connections');

    await tools.evaluate(`document.querySelector('#server-restart-modal button').click()`);
    await waitFor(() => tools.evaluate(`Boolean(window.__webmuxSmokeChangedAsset) && window.app?.serverRunId === '${serverInfo.serverRunId}'`), 'refreshed client after static change');
    await tools.evaluate(`(() => {
        const group = Array.from(window.app.groups.values()).find(candidate => candidate.paneIds.includes('${pane.id}'));
        window.app.activateGroup(group.id, '${pane.id}');
    })()`);
    await waitFor(() => tools.evaluate(`window.app.terminals.size === 1`), 'terminal after refreshed client');
    assert.equal(await tools.evaluate(`window.__webmuxSmokeDocumentToken`), undefined, 'refresh should replace the stale document');
    assert.equal(await tools.evaluate(`Boolean(document.getElementById('server-restart-modal'))`), false, 'refresh modal should be gone after reload');
    assert.deepEqual(tools.exceptions, [], `restart exceptions: ${tools.exceptions.join('; ')}`);
    assert.deepEqual(tools.consoleErrors, [], `restart console errors: ${tools.consoleErrors.join('; ')}`);
    assert.deepEqual(tools.scriptFailures, [], `restart script failures: ${tools.scriptFailures.join('; ')}`);

    const closeAllUI = await tools.evaluate(`(() => {
        const button = document.getElementById('close-all');
        button.click();
        const pending = {
            text: button.textContent,
            confirming: button.classList.contains('close-all-confirm'),
            flyoutVisible: getComputedStyle(document.getElementById('close-all-menu')).visibility === 'visible',
        };
        document.body.click();
        return {
            pending,
            dismissedText: button.textContent,
            dismissed: !button.classList.contains('close-all-confirm'),
        };
    })()`);
    assert.deepEqual(closeAllUI, {
        pending: { text: 'Close All?', confirming: true, flyoutVisible: true },
        dismissedText: 'Close All',
        dismissed: true,
    }, 'Close All should require confirmation and dismiss it on an outside click');
    assert.equal(await tools.evaluate(`document.getElementById('close-all-wrapper').parentElement.className`), 'pane-list-footer', 'Close All should remain in the pane list section');

    const paneAreaDismissal = await tools.evaluate(`(() => {
        const closeAll = document.getElementById('close-all');
        const newPaneToggle = document.querySelector('.new-pane-toggle');
        const paneMenuToggle = document.querySelector('.pane-menu-toggle');
        newPaneToggle.click();
        paneMenuToggle.click();
        closeAll.click();
        const before = {
            closeAll: closeAll.classList.contains('close-all-confirm'),
            newPane: !document.querySelector('.new-pane-menu').classList.contains('hidden'),
            paneMenu: !document.querySelector('.pane-action-menu').classList.contains('hidden'),
        };
        document.getElementById('pane-${pane.id}').dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
        return {
            before,
            after: {
                closeAll: closeAll.classList.contains('close-all-confirm'),
                newPane: !document.querySelector('.new-pane-menu').classList.contains('hidden'),
                paneMenu: !document.querySelector('.pane-action-menu').classList.contains('hidden'),
            },
        };
    })()`);
    assert.deepEqual(paneAreaDismissal, {
        before: { closeAll: true, newPane: true, paneMenu: true },
        after: { closeAll: false, newPane: false, paneMenu: false },
    }, 'pane-area interaction should dismiss transient pane controls');

    const closeAllRect = await tools.evaluate(`(() => {
        const rect = document.getElementById('close-all').getBoundingClientRect();
        return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
    })()`);
    await tools.command('Input.dispatchMouseEvent', {
        type: 'mouseMoved',
        x: closeAllRect.x + closeAllRect.width / 2,
        y: closeAllRect.y + closeAllRect.height / 2,
    });
    await waitFor(() => tools.evaluate(`getComputedStyle(document.getElementById('close-all-menu')).visibility === 'visible'`), 'Close All hover flyout');
    const closeAllMenuRect = await tools.evaluate(`(() => {
        const rect = document.getElementById('close-all-menu').getBoundingClientRect();
        return { x: rect.x, y: rect.y, width: rect.width, height: rect.height };
    })()`);
    await tools.command('Input.dispatchMouseEvent', {
        type: 'mouseMoved',
        x: closeAllMenuRect.x + closeAllMenuRect.width / 2,
        y: closeAllMenuRect.y + closeAllMenuRect.height / 2,
    });
    assert.equal(await tools.evaluate(`getComputedStyle(document.getElementById('close-all-menu')).visibility`), 'visible', 'flyout should remain visible while hovered');

    const closeAllBehavior = await tools.evaluate(`(async () => {
        const originalClosePanes = window.app.closePanes;
        const originalClosePane = window.app.closePane;
        const originalReducedMotion = window.app.prefersReducedMotion;
        const originalAnimateSidebarPaneReflow = window.app.animateSidebarPaneReflow;
        const calls = [];
        window.app.closePanes = type => calls.push(type || 'all');
        const terminalButton = document.querySelector('[data-close-pane-type="terminal"]');
        terminalButton.click();
        const firstText = terminalButton.textContent;
        const heldOpen = document.getElementById('close-all-wrapper').classList.contains('confirming');
        terminalButton.click();

        const testPanes = [
            { id: 'smoke-terminal-top', type: 'terminal' },
            { id: 'smoke-opencode-middle', type: 'opencode' },
            { id: 'smoke-opencode-bottom', type: 'opencode' },
        ];
        const testRows = testPanes.map(pane => {
            window.app.panes.set(pane.id, pane);
            const row = document.createElement('div');
            row.className = 'pane-item';
            row.dataset.paneId = pane.id;
            document.getElementById('pane-list').appendChild(row);
            return row;
        });
        const ordered = window.app.getPaneIdsInSidebarOrder('opencode');
        const closed = [];
        let reflows = 0;
        window.app.closePanes = originalClosePanes;
        window.app.closePane = async (id, options) => closed.push({ id, preAnimated: options.preAnimated, skipReflow: options.skipReflow });
        window.app.animateSidebarPaneReflow = () => { reflows++; };
        window.app.prefersReducedMotion = () => true;
        await window.app.closePanes('opencode');

        testRows.forEach(row => row.remove());
        testPanes.forEach(pane => {
            window.app.panes.delete(pane.id);
            window.app.closingPaneIds.delete(pane.id);
        });
        window.app.closePane = originalClosePane;
        window.app.animateSidebarPaneReflow = originalAnimateSidebarPaneReflow;
        window.app.prefersReducedMotion = originalReducedMotion;
        return { calls, firstText, heldOpen, ordered, closed, reflows };
    })()`);
    assert.deepEqual(closeAllBehavior, {
        calls: ['terminal'],
        firstText: 'Close Terminal Panes?',
        heldOpen: true,
        ordered: [openCodePane.id, 'smoke-opencode-middle', 'smoke-opencode-bottom'],
        closed: [
            { id: 'smoke-opencode-bottom', preAnimated: true, skipReflow: true },
            { id: 'smoke-opencode-middle', preAnimated: true, skipReflow: true },
            { id: openCodePane.id, preAnimated: true, skipReflow: true },
        ],
        reflows: 1,
    }, 'typed Close All should confirm, filter, and close from bottom to top');

    const closeMotion = await tools.evaluate(`(async () => {
        const paneItem = document.querySelector('.pane-item[data-pane-id="${pane.id}"]');
        const paneContainer = document.getElementById('pane-${pane.id}');
        const originalReducedMotion = window.app.prefersReducedMotion;
        window.app.prefersReducedMotion = () => false;
        const animation = window.app.animatePaneClose('${pane.id}');
        const animated = paneItem.classList.contains('pane-closing') && paneContainer.classList.contains('pane-closing');
        await animation;
        paneItem.classList.remove('pane-closing');
        paneContainer.classList.remove('pane-closing');
        window.app.prefersReducedMotion = () => true;
        await window.app.animatePaneClose('${pane.id}');
        const reduced = !paneItem.classList.contains('pane-closing') && !paneContainer.classList.contains('pane-closing');
        window.app.prefersReducedMotion = originalReducedMotion;
        return { animated, reduced };
    })()`);
    assert.deepEqual(closeMotion, { animated: true, reduced: true }, 'pane close animation should respect reduced motion');

    const testURL = 'https://example.com/this/is/a/very/long/path/that/wraps/across/the/terminal/width';
    const inputResponse = await fetch(`${mountedURL}api/panes/${pane.id}/input`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sequence: [
            { type: 'text', value: `clear; printf '${testURL}\\n'` },
            { type: 'key', value: 'Enter' },
        ] }),
    });
    const inputBody = await inputResponse.text();
    assert.equal(inputResponse.status, 200, inputBody);

    const terminalState = await waitFor(() => tools.evaluate(`(() => {
        const terminal = [...window.app.terminals.values()][0]?.terminal;
        if (!terminal) return null;
        for (let y = 0; y < terminal.buffer.active.length - 1; y++) {
            const line = terminal.buffer.active.getLine(y);
            const next = terminal.buffer.active.getLine(y + 1);
            if (line?.translateToString(true).startsWith('https://') && next?.isWrapped) {
                const rect = document.querySelector('.xterm-screen').getBoundingClientRect();
                return {
                    row: y,
                    viewportY: terminal.buffer.active.viewportY,
                    rows: terminal.rows,
                    rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
                };
            }
        }
        return null;
    })()`), 'wrapped URL output');

    const cellHeight = terminalState.rect.height / terminalState.rows;
    await tools.command('Input.dispatchMouseEvent', {
        type: 'mouseMoved',
        x: terminalState.rect.x + 4,
        y: terminalState.rect.y + ((terminalState.row - terminalState.viewportY + 0.5) * cellHeight),
        modifiers: 2,
    });
    await waitFor(() => tools.evaluate('Boolean(document.querySelector(".xterm-cursor-pointer"))'), 'wrapped URL link hover');
    assert.deepEqual(tools.exceptions, [], `browser exceptions: ${tools.exceptions.join('; ')}`);
    assert.deepEqual(tools.consoleErrors, [], `console errors: ${tools.consoleErrors.join('; ')}`);
    assert.deepEqual(tools.scriptFailures, [], `script load failures: ${tools.scriptFailures.join('; ')}`);

    tools.clearErrors();
    await tools.command('Page.navigate', { url: `${mountedURL}p/${pane.id}/` });
    await waitFor(() => tools.evaluate('Boolean(document.querySelector(".xterm-screen"))'), 'pop-out terminal');
    assert.deepEqual(tools.exceptions, [], `pop-out exceptions: ${tools.exceptions.join('; ')}`);
    assert.deepEqual(tools.consoleErrors, [], `pop-out console errors: ${tools.consoleErrors.join('; ')}`);
    assert.deepEqual(tools.scriptFailures, [], `pop-out script load failures: ${tools.scriptFailures.join('; ')}`);
    tools.close();

    console.log(`Dev smoke test passed on isolated port ${serverPort}`);
} catch (error) {
    console.error(error.stack || error);
    if (serverLog) console.error(`\nwebmux output:\n${serverLog}`);
    if (chromiumLog) console.error(`\nChromium output:\n${chromiumLog}`);
    throw error;
} finally {
    await stopProcess(chromium, 'SIGTERM');
    await stopProcess(server, 'SIGQUIT');
    await rm(tempRoot, { recursive: true, force: true });
}
