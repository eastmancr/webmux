(async function() {
    'use strict';

    const match = window.location.pathname.match(/^(.*)\/p\/([^/]+)/);
    if (!match) return;
    const basePath = match[1];
    const paneId = decodeURIComponent(match[2]);
    const url = path => `${basePath}${path}`;
    const wsUrl = path => `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}${url(path)}`;

    const defaults = {
        base00: '#1e1e2e', base02: '#313244', base03: '#45475a', base04: '#585b70',
        base05: '#cdd6f4', base06: '#f5e0dc', base07: '#ffffff', base08: '#f38ba8',
        base0A: '#f9e2af', base0B: '#a6e3a1', base0C: '#94e2d5', base0D: '#89b4fa',
        base0E: '#cba6f7', base12: '#f38ba8', base13: '#f9e2af', base14: '#a6e3a1',
        base15: '#94e2d5', base16: '#89b4fa', base17: '#cba6f7',
    };
    let colors = defaults;
    try {
        const response = await fetch(url('/api/settings'));
        if (response.ok) colors = { ...defaults, ...(await response.json()).terminal };
    } catch (error) {}

    document.getElementById('terminal').style.setProperty('--terminal-background', colors.base00);

    const terminal = new Terminal({
        fontSize: 14,
        fontFamily: 'JetBrains Mono, Fira Code, SF Mono, Menlo, Monaco, Courier New, monospace',
        scrollback: 50000,
        rightClickSelectsWord: true,
        scrollOnUserInput: true,
        allowProposedApi: true,
        theme: {
            background: colors.base00, foreground: colors.base05, cursor: colors.base06,
            cursorAccent: colors.base00, selectionBackground: colors.base02,
            scrollbarSliderBackground: '#00000000', scrollbarSliderHoverBackground: '#00000000',
            scrollbarSliderActiveBackground: '#00000000', overviewRulerBorder: '#00000000',
            black: colors.base00, red: colors.base08, green: colors.base0B, yellow: colors.base0A,
            blue: colors.base0D, magenta: colors.base0E, cyan: colors.base0C, white: colors.base05,
            brightBlack: colors.base03, brightRed: colors.base12, brightGreen: colors.base14,
            brightYellow: colors.base13, brightBlue: colors.base16, brightMagenta: colors.base17,
            brightCyan: colors.base15, brightWhite: colors.base07,
        },
    });
    const fitAddon = new FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(document.getElementById('terminal'));
    try {
        const webglAddon = new WebglAddon.WebglAddon();
        webglAddon.onContextLoss(() => webglAddon.dispose());
        terminal.loadAddon(webglAddon);
    } catch (error) {}

    let socket = null;
    let reconnectTimer = null;
    const sendResize = () => {
        if (socket?.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: 'resize', cols: terminal.cols, rows: terminal.rows }));
        }
    };
    const fit = () => {
        try {
            fitAddon.fit();
            sendResize();
        } catch (error) {}
    };
    const connect = () => {
        socket = new WebSocket(wsUrl(`/api/panes/${encodeURIComponent(paneId)}/terminal`));
        socket.binaryType = 'arraybuffer';
        socket.onopen = () => {
            fit();
            terminal.focus();
        };
        socket.onmessage = event => {
            if (event.data instanceof ArrayBuffer) terminal.write(new Uint8Array(event.data));
            else if (event.data instanceof Blob) event.data.arrayBuffer().then(data => terminal.write(new Uint8Array(data)));
        };
        socket.onerror = () => socket.close();
        socket.onclose = () => {
            if (!reconnectTimer) {
                reconnectTimer = setTimeout(() => {
                    reconnectTimer = null;
                    connect();
                }, 1000);
            }
        };
    };

    terminal.onData(data => {
        if (socket?.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data));
    });
    terminal.onBinary(data => {
        if (socket?.readyState === WebSocket.OPEN) {
            socket.send(Uint8Array.from(data, char => char.charCodeAt(0)));
        }
    });
    terminal.onResize(sendResize);
    new ResizeObserver(fit).observe(document.body);
    window.addEventListener('focus', () => terminal.focus());
    connect();
    requestAnimationFrame(fit);

    let knownClipboardVersion = -1;
    const clipboardEvents = new WebSocket(wsUrl('/api/clipboard/events'));
    clipboardEvents.onmessage = async event => {
        try {
            const message = JSON.parse(event.data);
            const version = Number(message.version);
            if (message.type !== 'clipboard' || !Number.isSafeInteger(version)) return;
            if (knownClipboardVersion === -1) {
                knownClipboardVersion = version;
                return;
            }
            if (version === knownClipboardVersion || !document.hasFocus()) return;
            const response = await fetch(url('/api/clipboard'));
            if (!response.ok) return;
            await navigator.clipboard.writeText(await response.text());
            knownClipboardVersion = version;
            terminal.focus();
        } catch (error) {}
    };
})();
