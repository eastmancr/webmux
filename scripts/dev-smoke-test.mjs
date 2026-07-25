import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
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
    for (const name of ['config', 'data', 'state', 'uploads']) {
        await mkdir(path.join(tempRoot, name));
    }

    const build = spawnSync('go', ['build', '-tags', 'dev', '-o', binary, '.'], { cwd: root, stdio: 'inherit' });
    assert.equal(build.status, 0, 'development build failed');

    const [serverPort, panePort, debugPort] = await Promise.all([reservePort(), reservePort(), reservePort()]);
    const baseURL = `http://127.0.0.1:${serverPort}`;
    const mountedURL = `${baseURL}/webmux/`;
    const isolatedEnv = {
        ...process.env,
        HISTFILE: '/dev/null',
        WEBMUX_STATIC_DIR: path.join(root, 'static'),
        XDG_CONFIG_HOME: path.join(tempRoot, 'config'),
        XDG_DATA_HOME: path.join(tempRoot, 'data'),
        XDG_STATE_HOME: path.join(tempRoot, 'state'),
    };
    server = spawn(binary, [
        '-port', String(serverPort),
        '-pane-port-start', String(panePort),
        '-upload-dir', path.join(tempRoot, 'uploads'),
        '-close-panes-on-exit',
        root,
    ], { cwd: root, env: isolatedEnv, stdio: ['ignore', 'pipe', 'pipe'] });

    server.stdout.on('data', chunk => { serverLog += chunk; });
    server.stderr.on('data', chunk => { serverLog += chunk; });
    await waitFor(async () => (await fetch(`${baseURL}/api/info`)).ok, 'isolated webmux server');

    const paneResponse = await fetch(`${mountedURL}api/panes`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: 'terminal', name: 'browser-smoke-test' }),
    });
    const paneBody = await paneResponse.text();
    assert.equal(paneResponse.status, 201, paneBody);
    const pane = JSON.parse(paneBody);

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
    assert.deepEqual(tools.exceptions, [], `browser exceptions: ${tools.exceptions.join('; ')}`);
    assert.deepEqual(tools.consoleErrors, [], `console errors: ${tools.consoleErrors.join('; ')}`);
    assert.deepEqual(tools.scriptFailures, [], `script load failures: ${tools.scriptFailures.join('; ')}`);

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
