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
        rightClickSelectsWord: false,
        scrollOnUserInput: true,
        allowProposedApi: true,
        disableStdin: true,
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
    const host = document.getElementById('terminal');
    terminal.open(host);
    const writeClipboardText = async text => {
        const fallbackCopy = () => {
            const textarea = document.createElement('textarea');
            textarea.value = text;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.select();
            const copied = document.execCommand('copy');
            textarea.remove();
            terminal.focus();
            return copied;
        };
        if (!navigator.clipboard?.writeText) return fallbackCopy();
        try {
            await navigator.clipboard.writeText(text);
            return true;
        } catch (error) {
            return fallbackCopy();
        }
    };
    const copyTerminalSelection = () => {
        const selection = terminal.getSelection();
        if (!selection) return false;
        writeClipboardText(selection);
        return true;
    };
    let shiftSelecting = false;
    host.addEventListener('mousedown', event => {
        shiftSelecting = event.button === 0 && event.shiftKey;
    }, true);
    host.addEventListener('mouseup', event => {
        if (shiftSelecting && event.button === 0) copyTerminalSelection();
        shiftSelecting = false;
    }, true);
    terminal.attachCustomKeyEventHandler(event => {
        if (event.type !== 'keydown' || !event.ctrlKey || !event.shiftKey || event.key.toLowerCase() !== 'c') {
            return true;
        }
        return !copyTerminalSelection();
    });
    terminal.registerLinkProvider({
        provideLinks: (lineNumber, callback) => {
            const text = terminal.buffer.active.getLine(lineNumber - 1)?.translateToString(true) || '';
            const links = [];
            for (const match of text.matchAll(/https?:\/\/[^\s<>"'`\\]+/g)) {
                const url = match[0].replace(/[.,;:!?)}\]]+$/, '');
                if (!url) continue;
                links.push({
                    text: url,
                    range: {
                        start: { x: match.index + 1, y: lineNumber },
                        end: { x: match.index + url.length, y: lineNumber },
                    },
                    activate: (event, link) => {
                        if (event.ctrlKey) window.open(link, '_blank', 'noopener,noreferrer');
                    },
                });
            }
            callback(links.length ? links : undefined);
        },
    });
    try {
        const webglAddon = new WebglAddon.WebglAddon();
        webglAddon.onContextLoss(() => webglAddon.dispose());
        terminal.loadAddon(webglAddon);
    } catch (error) {}
    terminal.loadAddon(new ImageAddon.ImageAddon({
        enableSizeReports: true,
        pixelLimit: 4 * 1024 * 1024,
        sixelSupport: true,
        sixelScrolling: true,
        sixelPaletteLimit: 256,
        sixelSizeLimit: 8 * 1024 * 1024,
        storageLimit: 32,
        showPlaceholder: true,
        iipSupport: false,
    }));

    let socket = null;
    let reconnectTimer = null;
    const sendInput = data => {
        if (socket?.readyState !== WebSocket.OPEN) return;
        const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
        for (let offset = 0; offset < bytes.length; offset += 32 * 1024) {
            socket.send(bytes.subarray(offset, offset + 32 * 1024));
        }
    };
    const sendResize = () => {
        if (socket?.readyState === WebSocket.OPEN) {
            const rect = terminal.element?.querySelector('.xterm-screen')?.getBoundingClientRect();
            socket.send(JSON.stringify({
                type: 'resize',
                cols: terminal.cols,
                rows: terminal.rows,
                pixelWidth: Math.round(rect?.width || 0),
                pixelHeight: Math.round(rect?.height || 0),
            }));
        }
    };
    const fit = () => {
        try {
            fitAddon.fit();
            sendResize();
        } catch (error) {}
    };
    const connect = () => {
        const connection = new WebSocket(wsUrl(`/api/panes/${encodeURIComponent(paneId)}/terminal`));
        socket = connection;
        connection.binaryType = 'arraybuffer';
        connection.onopen = () => {
            if (socket !== connection) return;
            terminal.options.disableStdin = false;
            fit();
            terminal.focus();
        };
        connection.onmessage = event => {
            if (socket !== connection) return;
            if (event.data instanceof ArrayBuffer) terminal.write(new Uint8Array(event.data));
            else if (event.data instanceof Blob) event.data.arrayBuffer().then(data => terminal.write(new Uint8Array(data)));
        };
        connection.onerror = () => connection.close();
        connection.onclose = () => {
            if (socket !== connection) return;
            socket = null;
            terminal.options.disableStdin = true;
            if (!reconnectTimer) {
                reconnectTimer = setTimeout(() => {
                    reconnectTimer = null;
                    connect();
                }, 1000);
            }
        };
    };

    terminal.onData(data => {
        sendInput(new TextEncoder().encode(data));
    });
    terminal.onBinary(data => {
        sendInput(Uint8Array.from(data, char => char.charCodeAt(0)));
    });
    terminal.onResize(sendResize);
    new ResizeObserver(fit).observe(document.body);
    connect();
    requestAnimationFrame(fit);

    let knownClipboardVersion = -1;
    let pendingClipboardVersion = -1;
    let syncingClipboard = false;
    let clipboardSocket = null;
    let clipboardReconnectTimer = null;
    const syncClipboard = async version => {
        pendingClipboardVersion = Math.max(pendingClipboardVersion, version);
        if (syncingClipboard || !document.hasFocus()) return;
        syncingClipboard = true;
        try {
            while (pendingClipboardVersion > knownClipboardVersion && document.hasFocus()) {
                const targetVersion = pendingClipboardVersion;
                const response = await fetch(url('/api/clipboard'));
                if (!response.ok || !await writeClipboardText(await response.text())) return;
                knownClipboardVersion = Math.max(knownClipboardVersion, targetVersion);
                if (pendingClipboardVersion <= knownClipboardVersion) pendingClipboardVersion = -1;
                terminal.focus();
            }
        } catch (error) {
        } finally {
            syncingClipboard = false;
        }
    };
    const connectClipboard = () => {
        const connection = new WebSocket(wsUrl('/api/clipboard/events'));
        clipboardSocket = connection;
        connection.onmessage = event => {
            try {
                const message = JSON.parse(event.data);
                const version = Number(message.version);
                if (message.type !== 'clipboard' || !Number.isSafeInteger(version)) return;
                if (knownClipboardVersion === -1) {
                    knownClipboardVersion = version;
                    return;
                }
                if (version !== knownClipboardVersion) syncClipboard(version);
            } catch (error) {}
        };
        connection.onerror = () => connection.close();
        connection.onclose = () => {
            if (clipboardSocket !== connection) return;
            clipboardSocket = null;
            if (!clipboardReconnectTimer) {
                clipboardReconnectTimer = setTimeout(() => {
                    clipboardReconnectTimer = null;
                    connectClipboard();
                }, 2000);
            }
        };
    };
    window.addEventListener('focus', () => {
        terminal.focus();
        if (pendingClipboardVersion > knownClipboardVersion) syncClipboard(pendingClipboardVersion);
    });
    connectClipboard();
})();
