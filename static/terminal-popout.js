(async function() {
    'use strict';

    const match = window.location.pathname.match(/^(.*)\/p\/([^/]+)/);
    if (!match) return;
    const basePath = match[1];
    const paneId = decodeURIComponent(match[2]);
    const url = path => `${basePath}${path}`;
    const wsUrl = path => `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}${url(path)}`;

    const registerTerminalLinks = terminal => {
        const urlPattern = /https?:\/\/[^\s<>"'`\\]+/g;
        const trailingPunctuation = /[.,;:!?)}\]]+$/;

        const wrappedLineText = lineNumber => {
            const buffer = terminal.buffer.active;
            let first = lineNumber - 1;
            let last = first;
            while (first > 0 && buffer.getLine(first)?.isWrapped) first--;
            while (buffer.getLine(last + 1)?.isWrapped) last++;

            const rows = [];
            for (let row = first; row <= last; row++) {
                rows.push(buffer.getLine(row)?.translateToString(true) || '');
            }
            return { text: rows.join(''), first };
        };

        const mapStringIndex = (row, column, length) => {
            const buffer = terminal.buffer.active;
            const cell = buffer.getNullCell();
            while (length > 0) {
                const line = buffer.getLine(row);
                if (!line) return null;
                for (let x = column; x < line.length; x++) {
                    line.getCell(x, cell);
                    const chars = cell.getChars();
                    if (!cell.getWidth()) continue;
                    length -= chars.length || 1;
                    if (x === line.length - 1 && !chars) {
                        const nextLine = buffer.getLine(row + 1);
                        if (nextLine?.isWrapped) {
                            nextLine.getCell(0, cell);
                            if (cell.getWidth() === 2) length++;
                        }
                    }
                    if (length < 0) return { row, column: x };
                }
                row++;
                column = 0;
            }
            return { row, column };
        };

        return terminal.registerLinkProvider({
            provideLinks: (lineNumber, callback) => {
                const logicalLine = wrappedLineText(lineNumber);
                const links = [];
                for (const match of logicalLine.text.matchAll(urlPattern)) {
                    const link = match[0].replace(trailingPunctuation, '');
                    if (!link) continue;
                    const start = mapStringIndex(logicalLine.first, 0, match.index);
                    const end = start && mapStringIndex(start.row, start.column, link.length);
                    if (!start || !end) continue;
                    links.push({
                        text: link,
                        range: {
                            start: { x: start.column + 1, y: start.row + 1 },
                            end: { x: end.column, y: end.row + 1 },
                        },
                        activate: (event, value) => {
                            if (event.ctrlKey) window.open(value, '_blank', 'noopener,noreferrer');
                        },
                    });
                }
                callback(links.length ? links : undefined);
            },
        });
    };

    const defaults = {
        base00: '#1e1e2e', base02: '#313244', base03: '#45475a', base04: '#585b70',
        base05: '#cdd6f4', base06: '#f5e0dc', base07: '#ffffff', base08: '#f38ba8',
        base0A: '#f9e2af', base0B: '#a6e3a1', base0C: '#94e2d5', base0D: '#89b4fa',
        base0E: '#cba6f7', base12: '#f38ba8', base13: '#f9e2af', base14: '#a6e3a1',
        base15: '#94e2d5', base16: '#89b4fa', base17: '#cba6f7',
    };
    let colors = defaults;
    let keybarButtons = ['C-c', 'C-d', 'C-z', 'C-\\', 'C-l', 'C-r', 'C-u', 'C-w'];
    try {
        const response = await fetch(url('/api/settings'));
        if (response.ok) {
            const settings = await response.json();
            colors = { ...defaults, ...settings.terminal };
            if (Array.isArray(settings.keybar?.buttons)) keybarButtons = settings.keybar.buttons;
            const uiVariables = {
                bgPrimary: '--bg-primary', bgSecondary: '--bg-secondary', bgTertiary: '--bg-tertiary',
                textPrimary: '--text-primary', accent: '--accent', border: '--border',
            };
            for (const [key, variable] of Object.entries(uiVariables)) {
                if (settings.ui?.[key]) document.documentElement.style.setProperty(variable, settings.ui[key]);
            }
        }
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
    host.addEventListener('paste', async event => {
        const files = Array.from(event.clipboardData?.files || []);
        if (files.length === 0) return;
        event.preventDefault();
        event.stopPropagation();
        const formData = new FormData();
        for (const file of files) formData.append('files', file, file.name || 'clipboard-data');
        try {
            const response = await fetch(url('/api/clipboard/files'), { method: 'POST', body: formData });
            if (!response.ok) throw new Error(`upload failed (${response.status})`);
            const result = await response.json();
            if (Array.isArray(result.uploaded) && result.uploaded.length > 0) {
                const quotedPaths = result.uploaded.map(path => /^[A-Za-z0-9_./-]+$/.test(path)
                    ? path
                    : `'${path.replaceAll("'", "'\\''")}'`);
                terminal.paste(quotedPaths.join(' '));
            }
        } catch (error) {
            console.error('[clipboard] File paste failed:', error);
        } finally {
            terminal.focus();
        }
    }, true);
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
        if (shiftSelecting && event.button === 0 && copyTerminalSelection()) {
            terminal.clearSelection();
        }
        shiftSelecting = false;
    }, true);
    terminal.attachCustomKeyEventHandler(event => {
        if (event.type !== 'keydown' || !event.ctrlKey || !event.shiftKey || event.key.toLowerCase() !== 'c') {
            return true;
        }
        return !copyTerminalSelection();
    });
    registerTerminalLinks(terminal);
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
    const formatKeyLabel = keys => keys
        .replace(/^C-/, 'Ctrl-')
        .replace(/^M-/, 'Alt-')
        .replace(/^S-/, 'Shift-')
        .replace(/Ctrl-M-/, 'Ctrl-Alt-')
        .replace(/Ctrl-S-/, 'Ctrl-Shift-')
        .replace(/Alt-S-/, 'Alt-Shift-')
        .replace(/Ctrl-Alt-S-/, 'Ctrl-Alt-Shift-');
    const sendKeybarInput = async keys => {
        let payload = { keys: [keys] };
        if (keys === 'Paste') {
            const clipboardResponse = await fetch(url('/api/clipboard/request?type=text/plain'));
            if (!clipboardResponse.ok) return;
            payload = { sequence: [{ type: 'text', value: await clipboardResponse.text() }] };
        }
        try {
            await fetch(url(`/api/panes/${encodeURIComponent(paneId)}/input`), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
        } finally {
            terminal.focus();
        }
    };
    const keybar = document.getElementById('keybar');
    for (const keys of keybarButtons.filter(key => typeof key === 'string' && key)) {
        const button = document.createElement('button');
        button.className = 'keybar-btn';
        button.type = 'button';
        button.textContent = formatKeyLabel(keys);
        button.title = formatKeyLabel(keys);
        button.addEventListener('pointerdown', event => event.preventDefault());
        button.addEventListener('click', () => sendKeybarInput(keys));
        keybar.appendChild(button);
    }
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
    if ('BroadcastChannel' in window) {
        const keybarChannel = new BroadcastChannel('webmux-popouts');
        terminal.onBell(() => {
            if (!document.hidden && document.hasFocus()) return;
            keybarChannel.postMessage({
                type: 'webmux-pane-attention',
                paneId,
                attentionEvent: 'terminal.bell',
            });
        });
        const requestKeybarState = () => keybarChannel.postMessage({
            type: 'webmux-keybar-state-request',
            paneId,
        });
        keybarChannel.onmessage = event => {
            const message = event.data;
            if (message?.type !== 'webmux-keybar-visibility') return;
            if (!message.applyToAll && message.paneId !== paneId) return;
            keybar.classList.toggle('user-hidden', message.hidden === true);
            keybar.setAttribute('aria-hidden', String(message.hidden === true));
            requestAnimationFrame(fit);
        };
        requestKeybarState();
        window.addEventListener('focus', requestKeybarState);
    } else {
        keybar.classList.remove('user-hidden');
        keybar.setAttribute('aria-hidden', 'false');
    }
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
    if ('ResizeObserver' in window) new ResizeObserver(fit).observe(document.body);
    else window.addEventListener('resize', fit);
    connect();
    requestAnimationFrame(fit);

    let knownClipboardVersion = -1;
    let pendingClipboardVersion = -1;
    let syncingClipboard = false;
    let clipboardSocket = null;
    let clipboardReconnectTimer = null;
    let clipboardRequestPrompt = null;
    const clipboardFormData = data => {
        const formData = new FormData();
        const addedTypes = new Set();
        for (const item of Array.from(data?.items || [])) {
            if (item.kind === 'file') {
                const file = item.getAsFile();
                if (file) {
                    formData.append('data', file, file.name || 'clipboard-data');
                    if (file.type) addedTypes.add(file.type);
                }
            } else if (item.kind === 'string' && item.type) {
                const text = data.getData(item.type);
                formData.append('data', new Blob([text], { type: item.type }), 'clipboard-text');
                addedTypes.add(item.type);
            }
        }
        for (const type of Array.from(data?.types || [])) {
            if (!type || type === 'Files' || addedTypes.has(type)) continue;
            formData.append('data', new Blob([data.getData(type)], { type }), 'clipboard-text');
        }
        return formData;
    };
    const showClipboardRequest = request => {
        if (!document.hasFocus() || !request.requestId) return;
        if (clipboardRequestPrompt) return;
        const prompt = document.createElement('div');
        prompt.className = 'clipboard-request-prompt';
        prompt.contentEditable = 'true';
        prompt.tabIndex = 0;
        prompt.setAttribute('role', 'textbox');
        prompt.setAttribute('aria-label', 'Paste clipboard contents');
        prompt.textContent = 'Paste clipboard now';

        const screen = terminal.element?.querySelector('.xterm-screen');
        const rect = screen?.getBoundingClientRect();
        if (rect) {
            const buffer = terminal.buffer.active;
            const row = Math.max(0, Math.min(terminal.rows - 1, buffer.baseY + buffer.cursorY - buffer.viewportY));
            prompt.style.left = `${Math.min(window.innerWidth - 232, Math.max(8, rect.left + buffer.cursorX * rect.width / Math.max(terminal.cols, 1)))}px`;
            prompt.style.top = `${Math.min(window.innerHeight - 72, Math.max(8, rect.top + (row + 1) * rect.height / Math.max(terminal.rows, 1)))}px`;
        }
        document.body.appendChild(prompt);
        clipboardRequestPrompt = prompt;
        const close = () => {
            if (clipboardRequestPrompt === prompt) clipboardRequestPrompt = null;
            prompt.remove();
            clearTimeout(timer);
            terminal.focus();
        };
        prompt.addEventListener('keydown', event => {
            if (event.key === 'Escape') {
                event.preventDefault();
                close();
            }
        });
        prompt.addEventListener('paste', async event => {
            event.preventDefault();
            event.stopPropagation();
            const formData = clipboardFormData(event.clipboardData);
            close();
            try {
                await fetch(url(`/api/clipboard/requests/${encodeURIComponent(request.requestId)}`), {
                    method: 'POST',
                    body: formData,
                });
            } catch (error) {}
        });
        const timer = setTimeout(close, 15000);
        requestAnimationFrame(() => prompt.focus());
    };
    const syncClipboard = async version => {
        pendingClipboardVersion = Math.max(pendingClipboardVersion, version);
        if (syncingClipboard || !document.hasFocus()) return;
        syncingClipboard = true;
        try {
            while (pendingClipboardVersion > knownClipboardVersion && document.hasFocus()) {
                const targetVersion = pendingClipboardVersion;
                const response = await fetch(url('/api/clipboard'));
                if (!response.ok) return;
                const contentType = (response.headers.get('Content-Type') || '').split(';', 1)[0].toLowerCase();
                if (contentType && contentType !== 'text/plain') {
                    knownClipboardVersion = Math.max(knownClipboardVersion, targetVersion);
                    continue;
                }
                if (!await writeClipboardText(await response.text())) return;
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
                if (message.type === 'clipboard-request') {
                    showClipboardRequest(message);
                    return;
                }
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
