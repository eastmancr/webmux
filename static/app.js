/*
 * Webmux - a browser-based pane multiplexer
 * Copyright (C) 2026  Webmux contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

// SECTION: CORE

// Terminal Multiplexer Application with Split Pane Support

function registerTerminalLinks(terminal) {
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
                const url = match[0].replace(trailingPunctuation, '');
                if (!url) continue;
                const start = mapStringIndex(logicalLine.first, 0, match.index);
                const end = start && mapStringIndex(start.row, start.column, url.length);
                if (!start || !end) continue;
                links.push({
                    text: url,
                    range: {
                        start: { x: start.column + 1, y: start.row + 1 },
                        end: { x: end.column, y: end.row + 1 },
                    },
                    activate: (event, link) => {
                        if (event.ctrlKey) window.open(link, '_blank', 'noopener,noreferrer');
                    },
                });
            }
            callback(links.length ? links : undefined);
        },
    });
}

class TerminalMultiplexer {
    constructor() {
        // Panes: individual pane definitions from the backend
        this.panes = new Map();
        this.closingPaneIds = new Set();
        this.pendingCloseAllButton = null;
        this.attentionPaneIds = new Set();
        this.baseDocumentTitle = document.title || 'Webmux';
        this.attentionSoundBlob = null;
        this.activeAttentionSounds = new Set();
        this.knownAudibleAttentionEvents = new Set(['opencode.notification']);

        // Groups: visual groupings of panes (1-4 panes per group)
        // Structure: { id, name, paneIds: [], layout: 'single'|'horizontal'|'vertical'|'grid', expandedQuadrant: null, splitRatio: [] }
        this.groups = new Map();

        // Ordered list of group IDs (for sidebar ordering)
        this.groupOrder = [];

        // Track which group is active
        this.activeGroupId = null;

        // Most-recently-used tab selections, including the active group.
        this.groupSelectionHistory = [];

        // Track which pane is focused within a split group (for keybar targeting)
        this.focusedPaneId = null;
        this.uiStateRevision = 0;
        this.uiStateSavePromise = Promise.resolve();

        // Drag state for sidebar
        this.draggedPaneId = null;
        this.draggedGroupId = null;

        // Group counter for unique IDs
        this.groupCounter = 0;

        // Track popped out windows: paneId -> Window object
        this.popoutWindows = new Map();
        this.popoutIntervals = new Map();
        this.popoutStates = new Map();
        this.pendingPopoutAlives = new Map();
        this.pendingPopoutCloses = new Map();
        this.popoutSuppressUntil = new Map();
        this.popoutChannel = null;
        this.popoutStorageKey = 'webmux.popouts';
        this.popoutStaleMs = 5000;

        // Shared backend panes reuse one live iframe so mirrors do not reconnect.
        this.sharedIframes = new Map();
        this.sharedIframePositionFrame = null;
        this.sharedIframeResizeObserver = null;
        this.pendingDedicatedIframeMounts = new Map();
        this.terminals = new Map();

        // Sidebar collapsed state
        this.sidebarCollapsed = false;

        // Server connection state
        this.serverConnected = true;
        this.serverRunId = null;
        this.assetVersion = null;
        this.serverRestartDetected = false;
        this.serverRecoveryPromise = null;
        this.connectionMode = 'active';
        this.paneEventsEnabled = true;
        this.scratchEventSource = null;
        this.scratchReconnectTimer = null;
        this.markedEventSource = null;
        this.markedReconnectTimer = null;
        this.clipboardSocket = null;
        this.clipboardReconnectTimer = null;
        this.clipboardFocusHandler = null;
        this.devReloadSocket = null;
        this.devReloadReconnectTimer = null;
        this.resumeLogsAfterRecovery = false;
        this.diagnosticQueue = [];
        this.diagnosticFlushTimer = null;
        this.paneTypes = [
            { type: 'terminal', label: 'Terminal', backendScope: 'dedicated', backendLifetime: 'pane', supportsKeybar: true, available: true },
            { type: 'opencode', label: 'OpenCode', backendScope: 'shared', backendLifetime: 'instance', supportsKeybar: false, available: true },
        ];
        // Debounce guard so a locally initiated backend restart and its
        // WebSocket broadcast do not reload iframes twice.
        this.lastBackendRestart = new Map();

        // Base path for proxy support (detected from current URL)
        this.basePath = this.detectBasePath();

        // Mobile mode detection
        this.mobileMode = false;
        this.mobileModeQuery = window.matchMedia('(max-width: 768px)');
        this.coarsePointerQuery = window.matchMedia('(pointer: coarse)');
        this.mobileTerminalMode = 'type';
        this.mobileScrollPaneId = null;

        // Mobile swipe navigation state
        this.swipeState = {
            isTracking: false,
            startX: 0,
            startY: 0,
            currentX: 0,
            currentY: 0,
            threshold: 50, // Minimum horizontal distance for swipe
            maxVerticalDeviation: 30, // Maximum vertical movement allowed
            triggered: false,
            suppressClick: false
        };

        this.init();
    }

    // Detect base path from current URL for proxy support
    // e.g., if accessed via /webmux/, basePath will be '/webmux'
    detectBasePath() {
        const path = window.location.pathname;
        // If path ends with index.html or /, strip it to get base
        // The app is served at the root of its path, so we look for
        // the path before any trailing slash or index.html
        let base = path.replace(/\/?(index\.html)?$/, '');
        // Ensure it doesn't end with a slash (we'll add slashes when building URLs)
        if (base.endsWith('/')) {
            base = base.slice(0, -1);
        }
        // Empty string means root path
        console.log('[webmux] Detected base path:', base || '(root)');
        return base;
    }

    // Build a URL with the base path prepended
    url(path) {
        // Ensure path starts with /
        if (!path.startsWith('/')) {
            path = '/' + path;
        }
        return this.basePath + path;
    }

    // Build a WebSocket URL with the base path
    wsUrl(path) {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        return `${protocol}//${window.location.host}${this.url(path)}`;
    }

    async init() {
        this.bindElements();
        this.bindEvents();
        this.setupPaneDragTarget();
        this.setupPopoutRegistry();

        // Setup mobile mode detection
        this.setupMobileModeDetection();

        // Setup mobile swipe navigation
        this.setupMobileSwipeNavigation();

        // Check server connection first
        const connected = await this.checkServerConnection();
        this.setServerConnected(connected);

        if (connected) {
            await this.loadSettings();
            await this.loadServerInfo();
            await this.loadUIState(); // Load saved UI state from server before loading panes
            await this.loadPanes();
        }

        if (this.groups.size === 0) {
            document.getElementById('no-pane').classList.remove('hidden');
            document.getElementById('keybar').classList.add('hidden');
            document.getElementById('toggle-keybar').classList.remove('active');
        }

        // Apply saved sidebar state
        if (this.sidebarCollapsed) {
            this.sidebar.classList.add('collapsed');
            this.startIconFadeTimer();
        }

        // Subscribe to pane updates (also checks connection)
        this.connectPaneEvents();

        window.addEventListener('pagehide', () => this.flushDiagnostics());
        window.addEventListener('focus', () => this.clearFocusedPaneAttention());
        document.addEventListener('visibilitychange', () => {
            if (!document.hidden && document.hasFocus()) this.clearFocusedPaneAttention();
        });

        // Connect to scratch pad SSE
        this.connectScratchEvents();

        // Connect to marked files SSE
        this.connectMarkedEvents();

        // Connect to clipboard events (for wm copy integration)
        this.connectClipboardEvents();
    }

    connectPaneEvents() {
        let reconnectTimer = null;
        let reconnectDelay = 250;

        const connect = () => {
            if (!this.paneEventsEnabled || this.connectionMode === 'refresh-required') return;
            const ws = new WebSocket(this.wsUrl('/api/panes/events'));
            let diagnosticPingTimer = null;
            this.paneSocket = ws;

            ws.onopen = () => {
                reconnectDelay = 250;
                this.logDiagnostic('pane-events', 'open', { path: '/api/panes/events' });
                if (this.settings?.diagnostics?.enabled && this.settings.diagnostics.optionalPing) {
                    const interval = Math.max(5, this.settings.diagnostics.pingIntervalSeconds || 30) * 1000;
                    diagnosticPingTimer = setInterval(() => {
                        if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'diagnostic-ping', ts: Date.now() }));
                    }, interval);
                }
            };

            ws.onmessage = (event) => {
                try {
                    this.handlePaneEvent(JSON.parse(event.data));
                } catch (err) {
                    console.warn('Failed to parse pane event:', err);
                }
            };

            ws.onclose = () => {
                if (diagnosticPingTimer) clearInterval(diagnosticPingTimer);
                this.logDiagnostic('pane-events', 'close', { path: '/api/panes/events' });
                if (this.paneSocket === ws) {
                    this.paneSocket = null;
                }
                this.beginServerRecovery();
                if (this.paneEventsEnabled && !reconnectTimer) {
                    const delay = reconnectDelay;
                    reconnectDelay = Math.min(reconnectDelay * 2, 2000);
                    reconnectTimer = setTimeout(() => {
                        reconnectTimer = null;
                        connect();
                    }, delay);
                }
            };

            ws.onerror = () => {
                this.logDiagnostic('pane-events', 'error', { path: '/api/panes/events' });
                ws.close();
            };
        };

        this.stopPaneEvents = () => {
            this.paneEventsEnabled = false;
            if (reconnectTimer) {
                clearTimeout(reconnectTimer);
                reconnectTimer = null;
            }
            const socket = this.paneSocket;
            this.paneSocket = null;
            socket?.close(1000, 'client refresh required');
        };
        connect();
    }

    // SECTION: MOBILE

    // Mobile Mode Detection
    // =====================

    setupMobileModeDetection() {
        // Initial check
        this.updateMobileMode();

        // Listen for changes in media queries
        this.mobileModeQuery.addEventListener('change', () => this.updateMobileMode());
        this.coarsePointerQuery.addEventListener('change', () => this.updateMobileMode());
    }

    updateMobileMode() {
        const isMobileViewport = this.mobileModeQuery.matches;
        const hasCoarsePointer = this.coarsePointerQuery.matches;
        const wasMobileMode = this.mobileMode;

        // Mobile mode is activated when either condition is true
        this.mobileMode = isMobileViewport || hasCoarsePointer;

        // Update body class for CSS styling
        document.body.classList.toggle('mobile-mode', this.mobileMode);

        // Handle mobile mode transitions
        if (this.mobileMode && !wasMobileMode) {
            this.enterMobileMode();
        } else if (!this.mobileMode && wasMobileMode) {
            this.exitMobileMode();
        }
    }

    enterMobileMode() {
        console.log('[webmux] Entering mobile mode');
        this.applyMobileTerminalMode();
        // Update mobile toolbar visibility
        this.updateMobileToolbar();
    }

    exitMobileMode() {
        console.log('[webmux] Exiting mobile mode');
        // Clean up mobile-specific state
        this.closeMobileDrawer();
        this.closeMobileKeySheet();
        this.applyMobileTerminalMode();
        this.updateMobileTerminalControls();
    }

    // Mobile Drawer
    // =============

    openMobileDrawer() {
        document.body.classList.add('mobile-drawer-open');
        // Create scrim if it doesn't exist
        this.ensureMobileScrim();
    }

    closeMobileDrawer() {
        document.body.classList.remove('mobile-drawer-open');
        this.removeMobileScrim();
    }

    toggleMobileDrawer() {
        if (document.body.classList.contains('mobile-drawer-open')) {
            this.closeMobileDrawer();
        } else {
            this.openMobileDrawer();
        }
    }

    ensureMobileScrim() {
        if (!document.getElementById('mobile-scrim')) {
            const scrim = document.createElement('div');
            scrim.id = 'mobile-scrim';
            scrim.className = 'mobile-scrim';
            scrim.addEventListener('click', () => this.closeMobileDrawer());
            document.body.appendChild(scrim);
        }
    }

    removeMobileScrim() {
        const scrim = document.getElementById('mobile-scrim');
        if (scrim) {
            scrim.remove();
        }
    }

    // Mobile Toolbar
    // ==============

    updateMobileToolbar() {
        if (!this.mobileMode) {
            // Hide mobile toolbar when not in mobile mode
            if (this.mobileBottomToolbar) {
                this.mobileBottomToolbar.classList.add('hidden');
            }
            return;
        }

        // Always show mobile toolbar in mobile mode
        if (this.mobileBottomToolbar) {
            this.mobileBottomToolbar.classList.remove('hidden');
        }

        const activeGroup = this.groups.get(this.activeGroupId);
        if (activeGroup && this.mobilePaneName) {
            // Update pane name display
            const paneId = activeGroup.paneIds.includes(this.focusedPaneId)
                ? this.focusedPaneId
                : this.getGroupPaneIdsInVisualOrder(activeGroup)[0];
            const pane = this.panes.get(paneId);
            this.mobilePaneName.textContent = pane ? this.getPaneDisplayName(pane) : 'Pane';
        } else if (this.mobilePaneName) {
            // No active pane
            this.mobilePaneName.textContent = 'Panes';
        }

        this.updateMobileKeybarVisibility();
        this.updateMobileTerminalControls();
    }

    setMobileTerminalMode(mode) {
        if (mode !== 'type' && mode !== 'scroll') return;
        this.mobileTerminalMode = mode;
        if (mode === 'type') this.mobileScrollPaneId = null;
        else {
            this.closeMobileKeySheet();
            this.terminals.forEach(entry => entry.terminal.blur());
        }
        this.applyMobileTerminalMode();
        this.updateMobileTerminalControls();
    }

    applyMobileTerminalMode() {
        const scrolling = this.mobileMode && this.mobileTerminalMode === 'scroll';
        this.terminals.forEach(entry => {
            entry.terminal.element?.closest('.terminal-host')?.classList.toggle('mobile-scroll-mode', scrolling);
        });
    }

    updateMobileTerminalControls() {
        const pane = this.panes.get(this.focusedPaneId);
        const terminalPane = pane?.type === 'terminal' && !this.isPanePoppedOut(pane);
        const scrolling = this.mobileMode && terminalPane && this.mobileTerminalMode === 'scroll';

        this.mobileTerminalModeBtn?.classList.toggle('hidden', !terminalPane);
        this.mobileArrowPad?.classList.toggle('hidden', !terminalPane);
        this.mobileKeysToggle?.classList.toggle('hidden', !terminalPane);
        if (!terminalPane) this.closeMobileKeySheet();

        if (this.mobileTerminalModeBtn) {
            this.mobileTerminalModeBtn.querySelector('.mobile-terminal-mode-label').textContent = scrolling ? 'Scroll' : 'Type';
            this.mobileTerminalModeBtn.querySelector('.mobile-mode-type-icon').classList.toggle('hidden', scrolling);
            this.mobileTerminalModeBtn.querySelector('.mobile-mode-scroll-icon').classList.toggle('hidden', !scrolling);
            this.mobileTerminalModeBtn.title = scrolling ? 'Switch to type mode' : 'Switch to scroll mode';
            this.mobileTerminalModeBtn.setAttribute('aria-label', `Terminal interaction mode: ${scrolling ? 'Scroll' : 'Type'}`);
            this.mobileTerminalModeBtn.classList.toggle('active', scrolling);
        }

        const activeGroup = this.groups.get(this.activeGroupId);
        const scrollPaneId = activeGroup?.paneIds.includes(this.mobileScrollPaneId)
            ? this.mobileScrollPaneId
            : this.focusedPaneId;
        const terminal = this.terminals.get(scrollPaneId)?.terminal;
        const behindLiveOutput = terminal && terminal.buffer.active.viewportY < terminal.buffer.active.baseY;
        this.mobileTerminalLive?.classList.toggle('hidden', !scrolling || !behindLiveOutput);
    }

    openMobileKeySheet() {
        if (this.panes.get(this.focusedPaneId)?.type !== 'terminal') return;
        this.mobileKeySheet?.classList.remove('hidden');
        this.mobileKeySheetScrim?.classList.remove('hidden');
        this.mobileKeysToggle?.setAttribute('aria-expanded', 'true');
    }

    closeMobileKeySheet() {
        this.mobileKeySheet?.classList.add('hidden');
        this.mobileKeySheetScrim?.classList.add('hidden');
        this.mobileKeysToggle?.setAttribute('aria-expanded', 'false');
    }

    toggleMobileKeySheet() {
        if (this.mobileKeySheet?.classList.contains('hidden')) this.openMobileKeySheet();
        else this.closeMobileKeySheet();
    }

    showMobilePanePicker() {
        // Open the mobile drawer to show the pane list
        this.openMobileDrawer();
    }

    // Mobile Swipe Navigation
    // =======================

    setupMobileSwipeNavigation() {
        // Only the pane/sidebar button owns quick pane switching gestures.
        if (!this.mobilePanePicker) return;

        this.mobilePanePicker.addEventListener('touchstart', (e) => this.handleSwipeStart(e), { passive: true });
        this.mobilePanePicker.addEventListener('touchmove', (e) => this.handleSwipeMove(e), { passive: true });
        this.mobilePanePicker.addEventListener('touchend', (e) => this.handleSwipeEnd(e));

        // Also track mouse events for testing on desktop
        this.mobilePanePicker.addEventListener('mousedown', (e) => this.handleSwipeStart(e));
        document.addEventListener('mousemove', (e) => this.handleSwipeMove(e));
        document.addEventListener('mouseup', (e) => this.handleSwipeEnd(e));
    }

    handleSwipeStart(e) {
        if (!this.mobileMode || this.groupOrder.length <= 1) return;

        this.swipeState.isTracking = true;
        this.swipeState.startX = this.getClientX(e);
        this.swipeState.startY = this.getClientY(e);
        this.swipeState.currentX = this.swipeState.startX;
        this.swipeState.currentY = this.swipeState.startY;
        this.swipeState.triggered = false;
    }

    handleSwipeMove(e) {
        if (!this.swipeState.isTracking) return;

        this.swipeState.currentX = this.getClientX(e);
        this.swipeState.currentY = this.getClientY(e);

        this.triggerMobileSwipeIfReady();
    }

    handleSwipeEnd(e) {
        if (!this.swipeState.isTracking) return;

        this.triggerMobileSwipeIfReady();
        this.swipeState.isTracking = false;
    }

    triggerMobileSwipeIfReady() {
        if (this.swipeState.triggered) return;

        const deltaX = this.swipeState.currentX - this.swipeState.startX;
        const deltaY = Math.abs(this.swipeState.currentY - this.swipeState.startY);

        // Check if this is a valid horizontal swipe
        if (Math.abs(deltaX) >= this.swipeState.threshold && deltaY <= this.swipeState.maxVerticalDeviation) {
            this.swipeState.triggered = true;
            this.swipeState.suppressClick = true;
            setTimeout(() => {
                this.swipeState.suppressClick = false;
            }, 300);
            if (deltaX > 0) {
                // Swipe right - previous group
                this.navigateGroup(-1);
            } else {
                // Swipe left - next group
                this.navigateGroup(1);
            }
        }
    }

    getClientX(e) {
        if (e.touches && e.touches.length > 0) {
            return e.touches[0].clientX;
        }
        return e.clientX;
    }

    getClientY(e) {
        if (e.touches && e.touches.length > 0) {
            return e.touches[0].clientY;
        }
        return e.clientY;
    }

    navigateGroup(direction) {
        if (this.groupOrder.length <= 1) return;

        const currentIndex = this.groupOrder.indexOf(this.activeGroupId);
        if (currentIndex === -1) return;

        let newIndex = currentIndex + direction;

        // Wrap around if needed
        if (newIndex < 0) {
            newIndex = this.groupOrder.length - 1;
        } else if (newIndex >= this.groupOrder.length) {
            newIndex = 0;
        }

        const newGroupId = this.groupOrder[newIndex];
        if (newGroupId && this.groups.has(newGroupId)) {
            this.activateGroup(newGroupId);

            // Show brief feedback about the group switch
            this.showGroupSwitchFeedback(newGroupId);
        }
    }

    showGroupSwitchFeedback(groupId) {
        const group = this.groups.get(groupId);
        if (!group) return;

        // Create a temporary toast notification
        const toast = document.createElement('div');
        toast.className = 'toast toast-info';
        toast.innerHTML = `
            <svg class="toast-icon" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true">
                <path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/>
            </svg>
            <span class="toast-message">Switched to: ${this.escapeHtml(this.getGroupDisplayName(group))}</span>
        `;

        const container = document.getElementById('toast-container');
        if (container) {
            container.appendChild(toast);

            // Auto-remove after 2 seconds
            setTimeout(() => {
                toast.classList.add('toast-out');
                setTimeout(() => toast.remove(), 200);
            }, 2000);
        }
    }

    // Mobile Marked Files
    // ===================

    toggleMobileMarkedDrawer() {
        if (!this.mobileMode) return;

        this.mobileMarkedDrawerOpen = !this.mobileMarkedDrawerOpen;

        if (this.mobileMarkedDrawer) {
            this.mobileMarkedDrawer.classList.toggle('visible', this.mobileMarkedDrawerOpen);
            this.mobileMarkedDrawer.classList.toggle('hidden', !this.mobileMarkedDrawerOpen);
        }
        if (this.mobileMarkedCount) {
            this.mobileMarkedCount.classList.toggle('expanded', this.mobileMarkedDrawerOpen);
        }
    }

    updateMobileMarkedUI() {
        if (!this.mobileMode) return;

        const hasMarkedFiles = this.markedFiles.length > 0;
        const downloadModalOpen = !this.downloadModal.classList.contains('hidden');

        // Show/hide mobile marked bar
        if (this.mobileMarkedBar) {
            this.mobileMarkedBar.classList.toggle('hidden', !hasMarkedFiles || !downloadModalOpen);
        }

        // Update count display
        if (this.mobileMarkedCount) {
            const countNumber = this.mobileMarkedCount.querySelector('.count-number');
            if (countNumber) {
                countNumber.textContent = this.markedFiles.length.toString();
            }
        }

        // Update mobile marked list
        this.renderMobileMarkedList();

        // Close drawer if no files left
        if (!hasMarkedFiles && this.mobileMarkedDrawerOpen) {
            this.mobileMarkedDrawerOpen = false;
            if (this.mobileMarkedDrawer) {
                this.mobileMarkedDrawer.classList.remove('visible');
                this.mobileMarkedDrawer.classList.add('hidden');
            }
            if (this.mobileMarkedCount) {
                this.mobileMarkedCount.classList.remove('expanded');
            }
        }
    }

    renderMobileMarkedList() {
        if (!this.mobileMarkedList) return;

        this.mobileMarkedList.innerHTML = this.markedFiles.map(file => {
            const icon = file.isDir
                ? '<path fill="currentColor" d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/>'
                : '<path fill="currentColor" d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/>';
            return `
            <div class="marked-item ${file.isDir ? 'directory' : ''}" data-path="${this.escapeHtml(file.path)}">
                <svg class="icon" viewBox="0 0 24 24" width="16" height="16">
                    ${icon}
                </svg>
                <span class="name" title="${this.escapeHtml(file.path)}">${this.escapeHtml(file.name)}</span>
                <button class="unmark-btn" title="Remove" data-path="${this.escapeHtml(file.path)}">
                    <svg viewBox="0 0 24 24" width="14" height="14">
                        <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                    </svg>
                </button>
            </div>
        `}).join('');

        // Bind unmark events
        this.mobileMarkedList.querySelectorAll('.unmark-btn').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                const path = btn.dataset.path;
                if (path) {
                    this.unmarkFile(path);
                }
            });
        });
    }

    // Server Connection
    // =================

    async checkServerConnection() {
        try {
            // Create abort controller for timeout
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), 5000);

            const response = await fetch(this.url('/api/info'), {
                method: 'GET',
                signal: controller.signal
            });

            clearTimeout(timeoutId);
            return response.ok;
        } catch (error) {
            return false;
        }
    }

    setServerConnected(connected, options = {}) {
        if (connected && this.connectionMode === 'refresh-required') return;
        // Skip if state hasn't changed
        if (connected === this.serverConnected) {
            return;
        }

        this.serverConnected = connected;
        this.logDiagnostic('server', connected ? 'connected' : 'disconnected');

        // Update UI to reflect connection state
        document.body.classList.toggle('server-disconnected', !connected);

        // Update button states
        this.updateActionButtonStates();

        // Show/hide disconnection warning
        if (!connected) {
            this.showDisconnectionWarning();
        } else {
            this.hideDisconnectionWarning();
            // Reload UI state and panes when reconnected
            if (options.reload !== false) {
                this.loadUIState().then(() => this.loadPanes());
            }
        }
    }

    handleServerReady(serverRunId, assetVersion) {
        if (this.connectionMode === 'refresh-required') return;
        if (this.observeServerIdentity(serverRunId, assetVersion)) return;
        if (this.connectionMode === 'recovering') {
            this.recoverAfterServerRestart(false);
        } else if (!this.serverConnected) {
            this.setServerConnected(true);
        } else {
            this.checkPaneHealth({ savePassiveChanges: false });
        }
    }

    observeServerIdentity(serverRunId, assetVersion) {
        if (typeof serverRunId !== 'string' || !serverRunId) return false;
        if (!this.serverRunId) {
            this.serverRunId = serverRunId;
            this.assetVersion = assetVersion || null;
            return false;
        }
        if (serverRunId === this.serverRunId) {
            if (!this.assetVersion && assetVersion) this.assetVersion = assetVersion;
            return false;
        }

        const assetsChanged = !this.assetVersion || !assetVersion || this.assetVersion !== assetVersion;
        this.serverRunId = serverRunId;
        this.assetVersion = assetVersion || null;
        if (assetsChanged) {
            this.showServerRestartModal();
        } else {
            this.recoverAfterServerRestart(true);
        }
        return true;
    }

    beginServerRecovery() {
        if (this.connectionMode === 'refresh-required' || this.connectionMode === 'recovering') return;
        this.connectionMode = 'recovering';
        this.setServerConnected(false);
        this.stopRestartSensitiveConnections();
    }

    stopRestartSensitiveConnections() {
        this.stopScratchEvents();
        this.stopMarkedEvents();
        this.stopClipboardEvents();
        this.stopDevReload();
        this.resumeLogsAfterRecovery = !this.logsModal?.classList.contains('hidden') && this.logsAutoRefresh?.checked;
        this.stopLogsAutoRefresh();
        this.resetPaneDisplayDOM();
    }

    startRestartSensitiveConnections() {
        this.connectScratchEvents();
        this.connectMarkedEvents();
        this.startClipboardWebSocket();
        this.connectDevReload();
        if (this.resumeLogsAfterRecovery) this.startLogsAutoRefresh();
        this.resumeLogsAfterRecovery = false;
    }

    recoverAfterServerRestart(showToast = true) {
        if (this.serverRecoveryPromise) return this.serverRecoveryPromise;
        this.serverRecoveryPromise = (async () => {
            this.connectionMode = 'active';
            await this.loadSettings();
            await this.loadServerInfo();
            await this.loadUIState();
            await this.loadPanes();
            if (this.connectionMode === 'refresh-required') return;

            for (const [popoutKey, state] of this.popoutStates) {
                const pane = this.panes.get(state.paneId);
                if (!pane) continue;
                const popoutWindow = this.popoutWindows.get(popoutKey);
                if (popoutWindow && !popoutWindow.closed) {
                    popoutWindow.location.href = this.url(`/p/${pane.id}/`);
                } else {
                    this.popoutChannel?.postMessage({
                        type: 'webmux-popout-reload',
                        paneId: state.paneId,
                        popoutId: state.popoutId,
                    });
                }
            }
            this.startRestartSensitiveConnections();
            this.setServerConnected(true, { reload: false });
            if (showToast) this.showToast('Reconnected after server restart');
        })().catch(error => {
            console.error('Failed to recover after server restart:', error);
            this.connectionMode = 'recovering';
            this.setServerConnected(false);
        }).finally(() => {
            this.serverRecoveryPromise = null;
        });
        return this.serverRecoveryPromise;
    }

    showServerRestartModal() {
        if (this.serverRestartDetected) return;
        this.serverRestartDetected = true;
        this.connectionMode = 'refresh-required';
        this.setServerConnected(false);
        this.stopPaneEvents?.();
        this.stopRestartSensitiveConnections();
        this.closePopoutsForRefresh();

        const modal = document.createElement('div');
        modal.id = 'server-restart-modal';
        modal.className = 'modal';
        modal.setAttribute('role', 'alertdialog');
        modal.setAttribute('aria-modal', 'true');
        modal.setAttribute('aria-labelledby', 'server-restart-title');
        modal.setAttribute('aria-describedby', 'server-restart-description');
        modal.innerHTML = `
            <div class="modal-content modal-sm">
                <div class="modal-header">
                    <h3 id="server-restart-title">Server restarted</h3>
                </div>
                <div class="modal-body">
                    <p id="server-restart-description">Refresh this page to reconnect and load the latest Webmux client.</p>
                    <div class="modal-actions">
                        <button type="button" class="btn btn-primary">Refresh page</button>
                    </div>
                </div>
            </div>
        `;
        const refreshButton = modal.querySelector('button');
        refreshButton.addEventListener('click', () => window.location.reload());
        document.body.appendChild(modal);
        refreshButton.focus();
    }

    closePopoutsForRefresh() {
        for (const [key, state] of Array.from(this.popoutStates)) {
            this.popoutChannel?.postMessage({
                type: 'webmux-popout-close',
                paneId: state.paneId,
                popoutId: state.popoutId,
            });
            const popoutWindow = this.popoutWindows.get(key);
            if (popoutWindow && !popoutWindow.closed) popoutWindow.close();
            this.clearPopoutTracking(key);
        }
    }

    updateActionButtonStates() {
        const disabled = !this.serverConnected;

        // Disable buttons that require server connection
        const serverButtons = [
            this.openUploadBtn,
            this.openDownloadBtn,
            this.closeAllBtn,
            ...this.closeAllOptions,
        ];

        if (disabled) this.resetCloseAllConfirmation();

        this.newPaneSplits.forEach(split => {
            serverButtons.push(...split.querySelectorAll('button'));
        });

        serverButtons.forEach(btn => {
            if (btn) {
                btn.disabled = disabled;
                btn.classList.toggle('disabled', disabled);
            }
        });

        // Update sidebar action buttons
        this.paneList?.querySelectorAll('.action-btn').forEach(btn => {
            btn.disabled = disabled;
            btn.classList.toggle('disabled', disabled);
        });
    }

    showDisconnectionWarning() {
        // Create border element if it doesn't exist
        if (!document.getElementById('disconnection-border')) {
            const border = document.createElement('div');
            border.id = 'disconnection-border';
            border.className = 'disconnection-border';
            document.body.appendChild(border);
        }

        // Create notch element if it doesn't exist
        if (!document.getElementById('disconnection-notch')) {
            const notch = document.createElement('div');
            notch.id = 'disconnection-notch';
            notch.className = 'disconnection-notch';
            notch.setAttribute('role', 'alert');
            notch.setAttribute('aria-live', 'assertive');
            notch.innerHTML = `
                <div class="disconnection-notch-tab-left"></div>
                <div class="disconnection-notch-inner">
                    <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
                        <path fill="currentColor" d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/>
                    </svg>
                    <span>Server disconnected</span>
                </div>
                <div class="disconnection-notch-tab-right"></div>
            `;
            document.body.appendChild(notch);
        }

        // Trigger animation by adding visible class after browser paints initial state
        // Double rAF ensures the elements are rendered before we trigger the transition
        requestAnimationFrame(() => {
            requestAnimationFrame(() => {
                document.getElementById('disconnection-border')?.classList.add('visible');
                document.getElementById('disconnection-notch')?.classList.add('visible');
            });
        });
    }

    hideDisconnectionWarning() {
        const border = document.getElementById('disconnection-border');
        const notch = document.getElementById('disconnection-notch');

        if (border) border.classList.remove('visible');
        if (notch) notch.classList.remove('visible');

        // Remove elements after transition
        setTimeout(() => {
            border?.remove();
            notch?.remove();
        }, 300);
    }

    // SECTION: API

    // Server Connection
    // =================

    getUIState() {
        return {
            revision: this.uiStateRevision,
            groupOrder: this.groupOrder,
            groups: Array.from(this.groups.entries()).map(([id, g]) => ({
                id: g.id,
                name: g.name,
                paneIds: g.paneIds,
                layout: g.layout,
                expandedQuadrant: g.expandedQuadrant,
                splitRatio: g.splitRatio,
                cellMapping: g.cellMapping
            })),
            activeGroupId: this.activeGroupId,
            focusedPaneId: this.focusedPaneId,
            attentionPaneIds: Array.from(this.attentionPaneIds),
            sidebarCollapsed: this.sidebar?.classList.contains('collapsed') || false,
            groupCounter: this.groupCounter
        };
    }

    markUIStateSaved() {
        this.lastSavedUIStateJSON = JSON.stringify(this.getUIState());
    }

    async saveUIState() {
        this.uiStateSavePromise = this.uiStateSavePromise.then(async () => {
            let stateJSON = JSON.stringify(this.getUIState());
            if (stateJSON === this.lastSavedUIStateJSON) return;
            let response;
            let retriedRevision = false;
            for (let attempt = 0; attempt < 2; attempt++) {
                response = await fetch(this.url('/api/ui-state'), {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: stateJSON
                });
                if (response.status !== 409) break;
                const conflict = await response.json();
                if (!Number.isSafeInteger(conflict.revision) || conflict.revision < 1) break;
                this.logDiagnostic('ui-state', 'revision-conflict', {
                    data: { clientRevision: this.uiStateRevision, serverRevision: conflict.revision }
                });
                this.uiStateRevision = conflict.revision;
                retriedRevision = true;
                stateJSON = JSON.stringify(this.getUIState());
            }
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const savedState = await response.json();
            if (Number.isSafeInteger(savedState.revision) && savedState.revision > 0) {
                this.uiStateRevision = savedState.revision;
            }
            if (retriedRevision) {
                this.logDiagnostic('ui-state', 'revision-retry-success', {
                    data: { revision: this.uiStateRevision }
                });
            }
            this.lastSavedUIStateJSON = JSON.stringify(this.getUIState());
        }).catch(e => {
            console.warn('Failed to save UI state to server:', e);
        });
        return this.uiStateSavePromise;
    }

    async loadUIState() {
        // Clear legacy localStorage state (now stored on server)
        try {
            localStorage.removeItem('multiplexer-ui-state');
        } catch (e) {
            // Ignore localStorage errors
        }

        try {
            const response = await fetch(this.url('/api/ui-state'));
            if (!response.ok) return;

            const state = await response.json();

            // Validate state structure
            if (!state || typeof state !== 'object') return;
            if (state.groups && !Array.isArray(state.groups)) return;
            if (state.groupOrder && !Array.isArray(state.groupOrder)) return;

            // Deep validation of groups
            if (state.groups) {
                state.groups = state.groups.filter(g => {
                    if (!g || typeof g !== 'object') return false;
                    if (typeof g.id !== 'string') return false;
                    if (!Array.isArray(g.paneIds)) return false;
                    // Reset invalid layout values
                    if (g.layout && !['single', 'horizontal', 'vertical', 'grid'].includes(g.layout)) {
                        g.layout = 'single';
                    }
                    // Reset invalid expandedQuadrant values
                    if (g.expandedQuadrant !== undefined &&
                        g.expandedQuadrant !== null &&
                        !['top', 'bottom', 'left', 'right'].includes(g.expandedQuadrant)) {
                        g.expandedQuadrant = null;
                    }
                    return true;
                });
            }

            // Validate groupOrder contains only strings
            if (state.groupOrder) {
                state.groupOrder = state.groupOrder.filter(id => typeof id === 'string');
            }

            if (state.attentionPaneIds && !Array.isArray(state.attentionPaneIds)) return;
            state.attentionPaneIds = Array.isArray(state.attentionPaneIds)
                ? state.attentionPaneIds.filter(id => typeof id === 'string')
                : [];

            // Validate groupCounter is a safe positive integer
            if (typeof state.groupCounter !== 'number' ||
                !Number.isSafeInteger(state.groupCounter) ||
                state.groupCounter < 0) {
                state.groupCounter = 0;
            }

            // Restore state
            this.savedState = state;
            this.uiStateRevision = Number.isSafeInteger(state.revision) && state.revision > 0 ? state.revision : 0;
            this.sidebarCollapsed = !!state.sidebarCollapsed;
            this.groupCounter = state.groupCounter;
            if (typeof state.focusedPaneId !== 'string') state.focusedPaneId = '';
        } catch (e) {
            console.warn('Failed to load UI state from server:', e);
        }
    }

    async checkPaneHealth(options = {}) {
        const savePassiveChanges = options.savePassiveChanges === true;
        try {
            const response = await fetch(this.url('/api/panes'));
            if (this.connectionMode !== 'active') return;

            // Update connection status on successful response
            if (!this.serverConnected) {
                this.setServerConnected(true);
            }

            const panes = await response.json();
            if (this.connectionMode !== 'active') return;

            // Build a map of server panes and update pane data
            const serverPaneIds = new Set();
            let needsRefresh = false;

            for (const pane of panes) {
                serverPaneIds.add(pane.id);
                if (this.closingPaneIds.has(pane.id)) {
                    continue;
                }

                // Update pane data (including currentActivity)
                const existing = this.panes.get(pane.id);
                if (!existing) {
                    pane._addedAt = Date.now();
                    this.panes.set(pane.id, pane);
                    const group = this.createGroup([pane.id]);
                    this.addGroupToSidebar(group);
                    needsRefresh = true;
                    if (savePassiveChanges) this.saveUIState();
                    continue;
                }
                if (existing.currentActivity !== pane.currentActivity || existing.name !== pane.name || existing.type !== pane.type) {
                    Object.assign(existing, pane);
                    needsRefresh = true;
                }
            }

            // Refresh sidebar if process names changed
            if (needsRefresh) {
                this.refreshPaneTitles();
            }

            // Find and clean up panes that no longer exist on server
            const currentPaneIds = Array.from(this.panes.keys());
            for (const paneId of currentPaneIds) {
                if (!serverPaneIds.has(paneId)) {
                    // Don't remove panes that were just created (< 3 seconds ago)
                    // This prevents race conditions where the health check runs before
                    // the server has fully registered the pane
                    const pane = this.panes.get(paneId);
                    const addedAt = pane?._addedAt || 0;
                    if (Date.now() - addedAt < 3000) {
                        console.log(`Pane ${paneId} not on server but was just created, waiting...`);
                        continue;
                    }
                    console.log(`Pane ${paneId} no longer exists on server, cleaning up`);
                    this.handlePaneDied(paneId, { save: savePassiveChanges });
                }
            }

            for (const paneId of Array.from(this.closingPaneIds)) {
                if (!serverPaneIds.has(paneId)) {
                    this.closingPaneIds.delete(paneId);
                }
            }
        } catch (error) {
            // Update connection status on failure
            if (this.serverConnected) {
                this.setServerConnected(false);
            }
        }
    }

    handlePaneEvent(event) {
        if (!event || !event.type) return;

        if (event.type === 'ready') {
            this.handleServerReady(event.serverRunId, event.assetVersion);
            return;
        }

        if (event.type === 'server-shutdown') {
            this.beginServerRecovery();
            return;
        }

        if (event.type === 'deleted') {
            if (event.paneId) {
                this.closingPaneIds.delete(event.paneId);
                this.handlePaneDied(event.paneId, { save: false });
            }
            return;
        }

        if (event.type === 'backend-restarted') {
            if (event.backendId) {
                this.handleBackendRestarted(event.backendId);
            }
            return;
        }

        const pane = event.pane;
        if (!pane?.id || this.closingPaneIds.has(pane.id)) return;

        const existing = this.panes.get(pane.id);
        if (!existing) {
            pane._addedAt = Date.now();
            this.panes.set(pane.id, pane);
            const group = this.createGroup([pane.id]);
            this.addGroupToSidebar(group);
            this.refreshPaneTitles();
            return;
        }

        if (existing.currentActivity !== pane.currentActivity || existing.name !== pane.name || existing.type !== pane.type) {
            Object.assign(existing, pane);
            this.refreshPaneTitles();
        }
    }

    refreshSidebar() {
        // Re-render all groups in the sidebar
        for (const [groupId, group] of this.groups) {
            this.updateGroupInSidebar(group);
        }
    }

    refreshPaneTitles() {
        this.refreshSidebar();
        this.updateMobileToolbar();

        document.querySelectorAll('iframe.pane-iframe').forEach(iframe => {
            const paneId = iframe.dataset.activePaneId || iframe.dataset.paneId;
            const pane = this.panes.get(paneId);
            if (pane) iframe.title = `${this.getPaneTypeLabel(pane)} pane: ${this.getPaneDisplayName(pane)}`;
        });
    }

    handlePaneDied(paneId, options = {}) {
        const shouldSave = options.save !== false;
        // Guard: only process if we still have this pane
        if (!this.panes.has(paneId)) {
            return;
        }

        this.removePaneLocalState(paneId);

        // Remove from any group that contains it
        for (const [groupId, group] of this.groups) {
            const paneIndexInGroup = group.paneIds.indexOf(paneId);
            if (paneIndexInGroup === -1) continue;

            // Find which pane this pane was in (for selecting next in visual order)
            const cm = group.cellMapping || group.paneIds.map((_, i) => i);
            const paneIndex = cm.indexOf(paneIndexInGroup);

            if (!this.removePaneFromGroup(group, paneId)) break;

            if (group.paneIds.length === 0) {
                // Find the index of this group before removing it
                const groupIndex = this.groupOrder.indexOf(groupId);

                // Remove empty group
                this.groups.delete(groupId);
                this.groupOrder = this.groupOrder.filter(id => id !== groupId);
                this.forgetGroupSelection(groupId);
                document.getElementById(`group-${groupId}`)?.remove();

                if (this.activeGroupId === groupId) {
                    this.activeGroupId = null;
                    const previousSelection = this.getPreviousGroupSelection();
                    // Fall back to sidebar order when there is no selection history.
                    const fallbackIndex = Math.min(groupIndex, this.groupOrder.length - 1);
                    const fallbackGroupId = this.groupOrder[fallbackIndex];
                    if (previousSelection) {
                        this.activateGroup(previousSelection.groupId, previousSelection.paneId, { save: shouldSave });
                    } else if (fallbackGroupId) {
                        this.activateGroup(fallbackGroupId, null, { save: shouldSave });
                    } else {
                        this.focusedPaneId = null;
                        this.updatePaneLayout();
                        this.noPaneEl.classList.remove('hidden');
                        this.keybar.classList.add('hidden');
                        this.keybarToggle.classList.remove('active');
                        this.updateMobileKeybarVisibility();
                        // Keep expand button visible when no panes
                        this.clearIconFade?.();
                    }
                }
            } else {
                this.updateGroupLayout(group);
                this.updateGroupInSidebar(group);
                if (this.activeGroupId === groupId) {
                    this.updatePaneLayout();
                    // Focus the next pane in pane order, or previous if we closed the last
                    const newCm = group.cellMapping || group.paneIds.map((_, i) => i);
                    const nextPanePosition = Math.min(paneIndex, newCm.length - 1);
                    const nextPaneIndex = newCm[nextPanePosition];
                    if (nextPaneIndex !== undefined) {
                        this.focusPane(group.paneIds[nextPaneIndex], { save: shouldSave });
                    }
                }
            }
            break;
        }
        this.refreshPaneTitles();
        if (shouldSave) this.saveUIState();
    }

    bindElements() {
        this.sidebar = document.getElementById('sidebar');
        this.sidebarIcons = document.querySelector('.sidebar-icons');
        this.toggleSidebarBtn = document.getElementById('toggle-sidebar');
        this.openSettingsBtn = document.getElementById('open-settings');
        this.paneList = document.getElementById('pane-list');
        this.closeAllWrapper = document.getElementById('close-all-wrapper');
        this.closeAllBtn = document.getElementById('close-all');
        this.closeAllOptions = Array.from(document.querySelectorAll('.close-all-option'));
        this.newPaneSplits = Array.from(document.querySelectorAll('.new-pane-split'));
        this.paneDisplay = document.getElementById('panes');
        this.paneDisplayContainer = document.getElementById('pane-display-container');
        this.noPaneEl = document.getElementById('no-pane');

        this.sharedIframeLayer = document.createElement('div');
        this.sharedIframeLayer.className = 'shared-pane-iframe-layer';
        this.paneDisplay.appendChild(this.sharedIframeLayer);

        // Modals
        this.uploadModal = document.getElementById('upload-modal');
        this.openUploadBtn = document.getElementById('open-upload');
        this.dropZone = document.getElementById('drop-zone');
        this.fileInput = document.getElementById('file-input');
        this.browseFilesBtn = document.getElementById('browse-files');
        this.uploadDirectory = document.getElementById('upload-directory');
        this.uploadProgress = document.getElementById('upload-progress');
        this.uploadResults = document.getElementById('upload-results');
        this.downloadModal = document.getElementById('download-modal');
        this.openDownloadBtn = document.getElementById('open-download');
        this.currentPathInput = document.getElementById('current-path');
        this.goPathBtn = document.getElementById('go-path');
        this.fileList = document.getElementById('file-list');
        this.fileCountEl = document.getElementById('file-count');
        this.fileHeader = document.querySelector('.file-header');

        // File browser state
        this.currentFiles = [];
        this.fileSortBy = 'name';
        this.fileSortAsc = true;

        // Marked files
        this.markedSidekick = document.getElementById('marked-sidekick');
        this.markedList = document.getElementById('marked-list');
        this.clearMarkedBtn = document.getElementById('clear-marked');
        this.downloadAllMarkedBtn = document.getElementById('download-all-marked');
        this.markedFiles = [];
        this.markedEventSource = null;

        // Mobile marked files UI
        this.mobileMarkedBar = document.getElementById('mobile-marked-bar');
        this.mobileMarkedCount = document.getElementById('mobile-marked-count');
        this.mobileDownloadAll = document.getElementById('mobile-download-all');
        this.mobileMarkedDrawer = document.getElementById('mobile-marked-drawer');
        this.mobileMarkedList = document.getElementById('mobile-marked-list');
        this.mobileMarkedClear = document.getElementById('mobile-marked-clear');
        this.mobileMarkedDrawerOpen = false;

        // File info popup
        this.fileInfoPopup = document.getElementById('file-info-popup');
        this.fileInfoName = document.getElementById('file-info-name');
        this.fileInfoPath = document.getElementById('file-info-path');
        this.fileInfoSize = document.getElementById('file-info-size');
        this.fileInfoModified = document.getElementById('file-info-modified');
        this.fileInfoCopyBtn = document.getElementById('file-info-copy');
        this.fileInfoScratchBtn = document.getElementById('file-info-scratch');
        this.fileInfoCloseBtn = document.querySelector('.file-info-close');
        this.fileInfoIcon = document.querySelector('.file-info-icon');
        this.currentFileInfo = null; // Store current file data for actions

        // Inline rename state
        this.renamingPaneId = null;

        // Settings modal
        this.settingsModal = document.getElementById('settings-modal');
        this.settingsSaveBtn = document.getElementById('settings-save');
        this.settingsResetBtn = document.getElementById('settings-reset');
        this.settingsImportBtn = document.getElementById('settings-import');
        this.settingsExportBtn = document.getElementById('settings-export');
        this.settingsConfigActions = document.getElementById('settings-config-actions');
        this.opencodeStorageExportBtn = document.getElementById('opencode-storage-export');
        this.opencodeStorageExportToggle = document.getElementById('opencode-storage-export-toggle');
        this.opencodeStorageImportBtn = document.getElementById('opencode-storage-import');
        this.opencodeStorageImportToggle = document.getElementById('opencode-storage-import-toggle');
        this.opencodeStorageResetSessionBtn = document.getElementById('opencode-storage-reset-session');
        this.opencodeStorageClearBtn = document.getElementById('opencode-storage-clear');
        this.opencodeStorageFileInput = document.getElementById('opencode-storage-file');
        this.opencodeStorageKeyCount = document.getElementById('opencode-storage-key-count');
        this.opencodeStorageSize = document.getElementById('opencode-storage-size');
        this.opencodeStorageVersion = document.getElementById('opencode-storage-version');
        this.settings = null; // Will be loaded from server

        // Keybinds modal
        this.keybindsModal = document.getElementById('keybinds-modal');
        this.openKeybindsBtn = document.getElementById('open-keybinds');

        // Logs modal
        this.logsModal = document.getElementById('logs-modal');
        this.openLogsBtn = document.getElementById('open-logs');
        this.logsContent = document.getElementById('logs-content');
        this.logsAutoRefresh = document.getElementById('logs-auto-refresh');
        this.logsRefreshBtn = document.getElementById('logs-refresh');
        this.logsRefreshInterval = null;
        this.logsFetchPending = false;

        // Scratch pad toggle
        this.toggleScratchBtn = document.getElementById('toggle-scratch');

        // Keybar (special keys toolbar)
        this.keybar = document.getElementById('keybar');
        this.keybarToggle = document.getElementById('toggle-keybar');
        this.keybarUserHidden = false; // Track if user manually hid the keybar

        // Mobile bottom toolbar
        this.mobileBottomToolbar = document.getElementById('mobile-bottom-toolbar');
        this.mobilePanePicker = document.getElementById('mobile-pane-picker');
        this.mobilePaneName = document.querySelector('.mobile-pane-name');
        this.mobileArrowPad = document.getElementById('mobile-arrow-pad');
        this.mobileTerminalModeBtn = document.getElementById('mobile-terminal-mode');
        this.mobileTerminalLive = document.getElementById('mobile-terminal-live');
        this.mobileKeysToggle = document.getElementById('mobile-keys-toggle');
        this.mobileKeySheet = document.getElementById('mobile-key-sheet');
        this.mobileKeySheetScrim = document.getElementById('mobile-key-sheet-scrim');
        this.mobileKeySheetClose = document.getElementById('mobile-key-sheet-close');
        this.mobileScratchBtn = document.getElementById('mobile-scratch');

    }

    // SECTION: EVENTS

    bindEvents() {
        window.addEventListener('resize', () => this.scheduleSharedIframePosition());
        if ('ResizeObserver' in window) {
            this.sharedIframeResizeObserver = new ResizeObserver(() => {
                this.scheduleSharedIframePosition();
                this.positionDividerControl();
            });
            this.sharedIframeResizeObserver.observe(this.paneDisplay);
        }

        // Sidebar toggle
        this.toggleSidebarBtn.addEventListener('click', () => {
            if (this.mobileMode) {
                this.toggleMobileDrawer();
            } else {
                this.toggleSidebar();
            }
        });

        // Icon bar fade behavior when sidebar is collapsed
        let iconFadeTimeout = null;
        this.startIconFadeTimer = () => {
            clearTimeout(iconFadeTimeout);
            this.sidebarIcons.classList.remove('faded');
            // Don't fade if no panes active
            if (this.panes.size === 0) return;
            iconFadeTimeout = setTimeout(() => {
                if (this.sidebar.classList.contains('collapsed') && this.panes.size > 0) {
                    this.sidebarIcons.classList.add('faded');
                }
            }, 2000);
        };
        this.clearIconFade = () => {
            clearTimeout(iconFadeTimeout);
            this.sidebarIcons.classList.remove('faded');
        };
        this.sidebarIcons.addEventListener('mouseenter', this.clearIconFade);
        this.sidebarIcons.addEventListener('mouseleave', () => {
            if (this.sidebar.classList.contains('collapsed')) {
                this.startIconFadeTimer();
            }
        });

        // Settings button
        this.openSettingsBtn.addEventListener('click', () => {
            this.openSettingsModal();
        });

        // Keybinds button
        this.openKeybindsBtn.addEventListener('click', () => {
            this.openModal(this.keybindsModal);
        });

        // Logs button
        this.openLogsBtn.addEventListener('click', () => {
            this.openLogsModal();
        });

        // Logs refresh button
        this.logsRefreshBtn.addEventListener('click', () => {
            this.fetchLogs();
        });

        // Logs auto-refresh toggle
        this.logsAutoRefresh.addEventListener('change', () => {
            if (this.logsAutoRefresh.checked && !this.logsModal.classList.contains('hidden')) {
                this.startLogsAutoRefresh();
            } else {
                this.stopLogsAutoRefresh();
            }
        });

        // Scratch pad toggle button
        this.toggleScratchBtn.addEventListener('click', () => {
            this.toggleScratchPad();
            this.updateScratchButtonState();
        });

        // Global keyboard shortcuts
        document.addEventListener('keydown', (e) => {
            if (e.target instanceof Element && e.target.closest('.terminal-host')) return;
            // Ctrl+/ to open keybinds modal
            if (e.ctrlKey && e.key === '/') {
                e.preventDefault();
                this.openModal(this.keybindsModal);
            }
            // Ctrl+Shift+L to open logs modal
            if (e.ctrlKey && e.shiftKey && (e.key === 'L' || e.key === 'l')) {
                e.preventDefault();
                this.openLogsModal();
            }
        });

        // Settings modal events
        this.settingsSaveBtn.addEventListener('click', () => this.saveSettings());
        this.settingsResetBtn.addEventListener('click', () => this.resetSettings());
        this.settingsImportBtn.addEventListener('click', () => this.importSettings());
        this.settingsExportBtn.addEventListener('click', () => this.exportSettings());
        this.opencodeStorageExportBtn?.addEventListener('click', () => this.copyOpenCodeStorage());
        this.opencodeStorageExportToggle?.addEventListener('click', (e) => this.toggleStorageActionMenu(e));
        this.opencodeStorageImportBtn?.addEventListener('click', () => this.pasteOpenCodeStorage());
        this.opencodeStorageImportToggle?.addEventListener('click', (e) => this.toggleStorageActionMenu(e));
        this.opencodeStorageResetSessionBtn?.addEventListener('click', () => this.resetOpenCodeSessionState());
        this.opencodeStorageClearBtn?.addEventListener('click', () => this.clearOpenCodeStorage());
        this.opencodeStorageFileInput?.addEventListener('change', () => this.importOpenCodeStorage());
        this.settingsModal.querySelectorAll('.storage-action-menu').forEach(menu => {
            menu.addEventListener('click', (e) => this.handleStorageActionMenuClick(e));
        });

        // Settings tab switching
        this.settingsModal.querySelectorAll('.settings-tab').forEach(tab => {
            tab.addEventListener('click', () => {
                const tabName = tab.dataset.tab;
                this.settingsModal.querySelectorAll('.settings-tab').forEach(t => t.classList.remove('active'));
                this.settingsModal.querySelectorAll('.settings-panel').forEach(p => p.classList.remove('active'));
                tab.classList.add('active');
                this.settingsModal.querySelector(`[data-panel="${tabName}"]`).classList.add('active');
                this.settingsModal.querySelectorAll('.settings-tab').forEach(t => t.setAttribute('aria-selected', t === tab ? 'true' : 'false'));

                // Show/hide theme import/export buttons based on tab
                this.updateThemeActionsVisibility(tabName);
                if (tabName === 'storage') this.loadOpenCodeStorageSummary();
            });
        });
        document.getElementById('panes-attention-indicators')?.addEventListener('change', () => {
            this.syncAttentionSettingsDisabled();
        });
        document.getElementById('preview-attention-sound')?.addEventListener('click', () => {
            this.playAttentionSound(true);
        });

        // Keybar settings event listeners
        const addKeybarBtn = document.getElementById('add-keybar-btn');
        if (addKeybarBtn) {
            addKeybarBtn.addEventListener('click', () => this.addKeybarButton());
        }

        // Add enter key support for keybar input and clear error on input
        const keybarInput = document.getElementById('new-keybar-keys');
        if (keybarInput) {
            keybarInput.addEventListener('keypress', (e) => {
                if (e.key === 'Enter') {
                    this.addKeybarButton();
                }
            });
            keybarInput.addEventListener('input', () => {
                this.hideKeybarInputError();
            });
        }

        // Color input live preview - sync color picker and hex input
        this.settingsModal.querySelectorAll('input[type="color"]').forEach(colorInput => {
            const setting = colorInput.dataset.setting;
            const hexInput = this.settingsModal.querySelector(`[data-setting-hex="${setting}"]`);

            colorInput.addEventListener('input', () => {
                if (hexInput) hexInput.value = colorInput.value;
                this.previewSettings();
            });
        });

        this.settingsModal.querySelectorAll('input[data-setting-hex]').forEach(hexInput => {
            const setting = hexInput.dataset.settingHex;
            const colorInput = this.settingsModal.querySelector(`[data-setting="${setting}"]`);

            hexInput.addEventListener('input', () => {
                let val = hexInput.value;
                // Auto-add # if missing
                if (val && !val.startsWith('#')) {
                    val = '#' + val;
                    hexInput.value = val;
                }
                // Only update color picker if valid hex
                if (/^#[0-9A-Fa-f]{6}$/.test(val)) {
                    if (colorInput) colorInput.value = val;
                    this.previewSettings();
                }
            });
        });

        // Pane management
        this.bindNewPaneSplits();
        this.paneDisplayContainer.addEventListener('pointerdown', () => this.dismissPaneMenus(), true);
        window.addEventListener('blur', () => {
            setTimeout(() => {
                if (document.activeElement instanceof HTMLIFrameElement && this.paneDisplay.contains(document.activeElement)) {
                    this.dismissPaneMenus();
                }
            }, 0);
        });
        [this.closeAllBtn, ...this.closeAllOptions].forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                if (this.pendingCloseAllButton === btn) {
                    const paneType = btn.dataset.closePaneType || null;
                    this.resetCloseAllConfirmation();
                    this.closePanes(paneType);
                    return;
                }
                this.setCloseAllConfirmation(btn);
            });
        });
        document.addEventListener('click', (e) => {
            if (!e.target.closest('.close-all-confirm')) this.resetCloseAllConfirmation();
            const activeSplit = e.target.closest('.new-pane-split');
            this.closeNewPaneMenus(activeSplit);
            if (!e.target.closest('.storage-action-split')) {
                this.closeStorageActionMenus();
            }
            if (!e.target.closest('.pane-action-menu-wrapper')) {
                this.closePaneActionMenus();
            }
        });
        document.addEventListener('keydown', (e) => {
            if (e.target instanceof Element && e.target.closest('.terminal-host')) return;
            if (e.key !== 'Escape') return;
            this.resetCloseAllConfirmation();
            this.closeNewPaneMenus();
            this.closeStorageActionMenus();
            this.closePaneActionMenus();
        });

        window.addEventListener('message', (e) => {
            const msg = e.data;
            if (!msg) return;
            if (e.origin !== window.location.origin) return;
            const managedSource = Array.from(this.sharedIframes.values()).some(iframe => iframe.contentWindow === e.source);
            if (!managedSource) return;
            if (msg.type === 'webmux-clipboard-write') {
                const text = String(msg.text || '');
                fetch(this.url('/api/clipboard'), { method: 'POST', body: text }).catch(() => {});
                navigator.clipboard?.writeText(text).catch(() => {});
            } else if (msg.type === 'webmux-pane-attention') {
                this.handlePaneAttentionMessage(msg, e.source);
            }
        });

        // Upload modal
        this.openUploadBtn.addEventListener('click', () => this.openModal(this.uploadModal));
        this.dropZone.addEventListener('click', () => this.fileInput.click());
        this.browseFilesBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            this.fileInput.click();
        });
        this.fileInput.addEventListener('change', (e) => this.handleFileSelect(e));

        // Drag and drop for file upload
        this.dropZone.addEventListener('dragover', (e) => {
            e.preventDefault();
            this.dropZone.classList.add('drag-over');
        });
        this.dropZone.addEventListener('dragleave', () => {
            this.dropZone.classList.remove('drag-over');
        });
        this.dropZone.addEventListener('drop', (e) => {
            e.preventDefault();
            this.dropZone.classList.remove('drag-over');
            this.handleFileDrop(e);
        });

        // Download modal
        this.openDownloadBtn.addEventListener('click', () => {
            this.openModal(this.downloadModal);
            this.browsePath('');
        });
        this.goPathBtn.addEventListener('click', () => this.browsePath(this.currentPathInput.value));
        this.currentPathInput.addEventListener('keydown', (e) => {
            if (e.key === 'Enter') this.browsePath(this.currentPathInput.value);
        });

        // File browser column header sorting
        this.fileHeader.querySelectorAll('.sortable').forEach(col => {
            col.addEventListener('click', () => {
                const sortKey = col.dataset.sort;
                if (this.fileSortBy === sortKey) {
                    // Same column - toggle direction
                    this.fileSortAsc = !this.fileSortAsc;
                } else {
                    // New column - set ascending
                    this.fileSortBy = sortKey;
                    this.fileSortAsc = true;
                }
                this.updateSortIndicators();
                this.renderFileList();
            });
        });

        // Marked files
        this.clearMarkedBtn.addEventListener('click', () => this.clearMarkedFiles());
        this.downloadAllMarkedBtn.addEventListener('click', () => this.downloadMarkedFiles());

        // Mobile marked files UI
        if (this.mobileMarkedCount) {
            this.mobileMarkedCount.addEventListener('click', () => this.toggleMobileMarkedDrawer());
        }
        if (this.mobileDownloadAll) {
            this.mobileDownloadAll.addEventListener('click', () => this.downloadMarkedFiles());
        }
        if (this.mobileMarkedClear) {
            this.mobileMarkedClear.addEventListener('click', () => this.clearMarkedFiles());
        }

        // File info popup
        this.fileInfoCloseBtn.addEventListener('click', () => this.hideFileInfoPopup());
        this.fileInfoCopyBtn.addEventListener('click', () => this.copyFileInfoPath());
        this.fileInfoScratchBtn.addEventListener('click', () => this.sendFileInfoToScratch());
        // Close popup when clicking outside
        document.addEventListener('click', (e) => {
            if (!this.fileInfoPopup.classList.contains('hidden') &&
                !this.fileInfoPopup.contains(e.target) &&
                !e.target.closest('.file-item')) {
                this.hideFileInfoPopup();
            }
        });

        // Close modals
        document.querySelectorAll('.close-modal').forEach(btn => {
            btn.addEventListener('click', (e) => {
                const modal = btn.closest('.modal');
                if (modal === this.settingsModal) {
                    e.stopPropagation();
                    this.handleSettingsClose();
                } else if (modal === this.logsModal) {
                    this.closeLogsModal();
                } else {
                    this.closeModal(this.uploadModal);
                    this.closeModal(this.downloadModal);
                    this.closeModal(this.keybindsModal);
                }
            });
        });

        [this.uploadModal, this.downloadModal, this.settingsModal, this.keybindsModal, this.logsModal].forEach(modal => {
            modal.addEventListener('click', (e) => {
                if (e.target === modal) {
                    if (modal === this.settingsModal) {
                        // Clicking backdrop - if discard pending, reset it; otherwise try to close
                        if (this.settingsDiscardPending) {
                            this.resetSettingsDiscardState();
                        } else {
                            this.handleSettingsClose();
                        }
                    } else if (modal === this.logsModal) {
                        this.closeLogsModal();
                    } else {
                        this.closeModal(modal);
                    }
                }
            });
        });

        // Reset discard state when clicking inside settings modal content (but not on close button)
        this.settingsModal.querySelector('.modal-content')?.addEventListener('click', (e) => {
            if (!e.target.closest('.close-modal')) {
                this.resetSettingsDiscardState();
            }
        });

        // Keyboard shortcuts
        document.addEventListener('keydown', (e) => {
            if (e.target instanceof Element && e.target.closest('.terminal-host')) return;
            if (e.ctrlKey && e.shiftKey && (e.key === 'T' || e.key === 't')) {
                e.preventDefault();
                this.createNewPaneAndGroup();
            }

            if (e.key === 'Escape') {
                this.closeModal(this.uploadModal);
                this.closeModal(this.downloadModal);
                this.closeModal(this.keybindsModal);
                this.closeLogsModal();
                // For settings modal, treat Escape like clicking elsewhere
                if (!this.settingsModal.classList.contains('hidden')) {
                    if (this.settingsDiscardPending) {
                        this.resetSettingsDiscardState();
                    } else {
                        this.handleSettingsClose();
                    }
                }
                this.cancelInlineRename();
            }
        });

        // Global dragend to clean up state
        document.addEventListener('dragend', () => {
            this.draggedPaneId = null;
            this.hideDragOverlay();
            document.querySelectorAll('.dragging').forEach(el => el.classList.remove('dragging'));
        });

        // Keybar events will be bound dynamically after settings are loaded
        this.bindKeybarEvents();

        // Keybar toggle (in sidebar)
        this.keybarToggle.addEventListener('click', () => {
            // Only toggle if there are active panes
            if (this.groups.size > 0) {
                this.keybarUserHidden = !this.keybarUserHidden;
                this.updateKeybarVisibility();
                this.broadcastKeybarVisibility();
                this.scheduleSharedIframePosition();
            }
        });

        // Mobile toolbar event handlers
        if (this.mobilePanePicker) {
            this.mobilePanePicker.addEventListener('click', (e) => {
                if (this.swipeState.suppressClick) {
                    e.preventDefault();
                    this.swipeState.suppressClick = false;
                    return;
                }
                this.showMobilePanePicker();
            });
        }

        // Mobile keybar events will be bound dynamically after settings are loaded
        this.bindMobileKeybarEvents();
        this.bindMobileArrowPadEvents();

        this.mobileTerminalModeBtn?.addEventListener('click', () => {
            this.setMobileTerminalMode(this.mobileTerminalMode === 'scroll' ? 'type' : 'scroll');
            this.mobileTerminalModeBtn.blur();
        });
        this.mobileTerminalLive?.addEventListener('click', () => {
            const activeGroup = this.groups.get(this.activeGroupId);
            const paneId = activeGroup?.paneIds.includes(this.mobileScrollPaneId)
                ? this.mobileScrollPaneId
                : this.focusedPaneId;
            this.terminals.get(paneId)?.terminal.scrollToBottom();
            this.updateMobileTerminalControls();
            this.mobileTerminalLive.blur();
        });
        this.mobileKeysToggle?.addEventListener('click', () => this.toggleMobileKeySheet());
        this.mobileKeySheetClose?.addEventListener('click', () => this.closeMobileKeySheet());
        this.mobileKeySheetScrim?.addEventListener('click', () => this.closeMobileKeySheet());
        this.mobileKeySheet?.querySelectorAll('[data-keys]').forEach(button => {
            button.addEventListener('click', () => {
                this.sendInputToActivePane({ keys: [button.dataset.keys] });
                button.blur();
            });
        });

        // Mobile utility buttons
        if (this.mobileScratchBtn) {
            this.mobileScratchBtn.addEventListener('click', () => {
                this.toggleScratchPad();
                this.updateScratchButtonState();
                this.closeMobileKeySheet();
            });
        }
    }

    // SECTION: PANES

    // Group & Pane Management
    // ==========================

    async loadPanes() {
        try {
            this.logDiagnostic('panes', 'load-start');
            const response = await fetch(this.url('/api/panes'));
            const serverPanes = await response.json();
            if (this.connectionMode === 'refresh-required') return;

            // Clear existing state before loading
            this.panes.clear();
            this.attentionPaneIds.clear();
            this.updateAttentionTitle();
            this.groups.clear();
            this.groupOrder = [];
            this.activeGroupId = null;
            this.focusedPaneId = null;
            this.groupSelectionHistory = [];
            this.paneList.innerHTML = '';
            this.resetPaneDisplayDOM();

            // Build map of server panes
            const serverPaneMap = new Map();
            for (const pane of serverPanes) {
                serverPaneMap.set(pane.id, pane);
                this.panes.set(pane.id, pane);
            }
            this.attentionPaneIds = new Set(
                (this.savedState?.attentionPaneIds || []).filter(paneId => this.paneAttentionEnabled(this.panes.get(paneId)))
            );
            this.updateAttentionTitle();
            this.reconcilePopoutStates();

            // Try to restore saved state from server
            if (this.savedState && this.savedState.groups && this.savedState.groups.length > 0) {
                this.reconcileWithSavedState(serverPaneMap);
            } else {
                // No saved state - create a group for each pane
                for (const pane of serverPaneMap.values()) {
                    const group = this.createGroup([pane.id]);
                    this.addGroupToSidebar(group);
                }
            }

            if (this.groups.size > 0) {
                // Restore active group or pick first
                if (this.savedState?.activeGroupId && this.groups.has(this.savedState.activeGroupId)) {
                    this.activateGroup(this.savedState.activeGroupId, this.savedState.focusedPaneId, { save: false });
                } else {
                    const firstGroupId = this.groupOrder[0] || this.groups.keys().next().value;
                    if (firstGroupId) this.activateGroup(firstGroupId, null, { save: false });
                }
            } else {
                this.updatePaneLayout();
            }

            this.refreshPaneTitles();

            // Clear saved state after reconciliation
            this.savedState = null;
            this.markUIStateSaved();
            this.logDiagnostic('panes', 'load-complete', { data: { count: serverPanes.length } });
        } catch (error) {
            this.logDiagnostic('panes', 'load-error', { data: { error: error.message } });
            console.error('Failed to load panes:', error);
        }
    }

    resetPaneDisplayDOM() {
        this.logDiagnostic('iframe', 'reset-dom', { data: { shared: this.sharedIframes.size } });
        for (const paneId of this.terminals.keys()) this.disposeTerminal(paneId);
        this.sharedIframes.forEach(iframe => iframe.remove());
        this.sharedIframes.clear();
        this.pendingDedicatedIframeMounts.forEach(frame => cancelAnimationFrame(frame));
        this.pendingDedicatedIframeMounts.clear();
        if (this.sharedIframePositionFrame) {
            cancelAnimationFrame(this.sharedIframePositionFrame);
            this.sharedIframePositionFrame = null;
        }

        this.paneDisplay.querySelectorAll('.pane-container, .split-divider, #divider-control, #resize-overlay, #drag-capture-overlay').forEach(el => el.remove());
        if (this.sharedIframeLayer.parentElement !== this.paneDisplay) {
            this.paneDisplay.appendChild(this.sharedIframeLayer);
        }
    }

    reconcileWithSavedState(serverPaneMap) {
        const savedGroups = this.savedState.groups || [];
        const savedOrder = this.savedState.groupOrder || [];
        const usedPaneIds = new Set();

        // Restore groups, filtering out dead panes
        for (const savedGroup of savedGroups) {
            const validPaneIds = savedGroup.paneIds.filter(id => serverPaneMap.has(id));

            if (validPaneIds.length === 0) continue;

            validPaneIds.forEach(id => usedPaneIds.add(id));

            // Recreate group with saved properties
            const samePaneCount = validPaneIds.length === savedGroup.paneIds.length;
            const group = {
                id: savedGroup.id,
                name: savedGroup.name,
                paneIds: validPaneIds,
                layout: samePaneCount ? savedGroup.layout : this.getDefaultLayout(validPaneIds.length),
                expandedQuadrant: savedGroup.expandedQuadrant,
                splitRatio: samePaneCount ? savedGroup.splitRatio : this.getDefaultSplitRatio(validPaneIds.length),
                cellMapping: samePaneCount ? savedGroup.cellMapping : null
            };

            this.groups.set(group.id, group);

            // Update groupCounter if needed
            const match = group.id.match(/^group-(\d+)$/);
            if (match) {
                this.groupCounter = Math.max(this.groupCounter, parseInt(match[1]));
            }
        }

        // Restore group order (filtering out deleted groups)
        this.groupOrder = savedOrder.filter(id => this.groups.has(id));

        // Add any new groups not in saved order
        for (const groupId of this.groups.keys()) {
            if (!this.groupOrder.includes(groupId)) {
                this.groupOrder.push(groupId);
            }
        }

        // Create groups for any panes not in saved state (new panes)
        for (const [paneId, pane] of serverPaneMap) {
            if (!usedPaneIds.has(paneId)) {
                this.createGroup([paneId]);
            }
        }

        // Render sidebar in order
        for (const groupId of this.groupOrder) {
            const group = this.groups.get(groupId);
            if (group) this.addGroupToSidebar(group);
        }
    }

    createGroup(paneIds, name = null) {
        const id = `group-${++this.groupCounter}`;
        const group = {
            id,
            name: name || this.generateGroupName(paneIds),
            paneIds: [...paneIds],
            layout: paneIds.length === 1 ? 'single' : this.getDefaultLayout(paneIds.length),
            expandedQuadrant: null,
            splitRatio: this.getDefaultSplitRatio(paneIds.length),
            cellMapping: null // null means identity mapping
        };
        this.groups.set(id, group);
        if (!this.groupOrder.includes(id)) {
            this.groupOrder.push(id);
        }
        return group;
    }

    getDefaultSplitRatio(count) {
        switch (count) {
            case 1: return null;
            case 2: return [0.5];
            case 3: return [0.5, 0.5];
            case 4: return [0.5, 0.5];
            default: return [0.5, 0.5];
        }
    }

    // Remove a pane from a group, updating cellMapping appropriately
    removePaneFromGroup(group, paneId) {
        const idx = group.paneIds.indexOf(paneId);
        if (idx === -1) return false;

        // Clear focused pane if it's the one being removed
        if (this.focusedPaneId === paneId) {
            this.focusedPaneId = null;
        }

        group.paneIds.splice(idx, 1);

        // Update cellMapping: remove the pane's pane and adjust remaining indices
        if (group.cellMapping) {
            const paneIdx = group.cellMapping.indexOf(idx);
            const newMapping = group.cellMapping
                .filter((_, i) => i !== paneIdx)
                .map(paneIdx => paneIdx > idx ? paneIdx - 1 : paneIdx);
            group.cellMapping = newMapping.length > 0 ? newMapping : null;
        }

        return true;
    }

    getGroupPaneIdsInVisualOrder(group) {
        if (!group || !Array.isArray(group.paneIds)) return [];

        const paneIds = group.paneIds;
        const mapping = Array.isArray(group.cellMapping)
            ? group.cellMapping
            : paneIds.map((_, i) => i);
        const ordered = [];
        const seen = new Set();

        // cellMapping is pane-position order: top-left through bottom-right.
        for (const paneIndex of mapping) {
            if (!Number.isInteger(paneIndex) || paneIndex < 0 || paneIndex >= paneIds.length) continue;
            const paneId = paneIds[paneIndex];
            if (!paneId || seen.has(paneId)) continue;
            ordered.push(paneId);
            seen.add(paneId);
        }

        for (const paneId of paneIds) {
            if (!seen.has(paneId)) ordered.push(paneId);
        }

        return ordered;
    }

    getPaneIdsInCanonicalOrder() {
        const ordered = [];
        const seen = new Set();

        for (const groupId of this.groupOrder) {
            const group = this.groups.get(groupId);
            for (const paneId of this.getGroupPaneIdsInVisualOrder(group)) {
                if (!this.panes.has(paneId) || seen.has(paneId)) continue;
                ordered.push(paneId);
                seen.add(paneId);
            }
        }

        for (const paneId of this.panes.keys()) {
            if (seen.has(paneId)) continue;
            ordered.push(paneId);
            seen.add(paneId);
        }

        return ordered;
    }

    getGroupDisplayName(group) {
        if (!group || group.paneIds.length === 0) return 'Panes';
        if (group.paneIds.length > 1) return `Split (${group.paneIds.length})`;
        return this.getPaneDisplayName(this.panes.get(group.paneIds[0]));
    }

    updateGroupLayout(group) {
        const count = group.paneIds.length;
        group.layout = this.getDefaultLayout(count);
        group.splitRatio = this.getDefaultSplitRatio(count);

        // Reset expandedQuadrant for 3-pane if not already set
        if (count === 3 && !group.expandedQuadrant) {
            group.expandedQuadrant = 'bottom';
        } else if (count !== 3) {
            group.expandedQuadrant = null;
        }
    }

    generateGroupName(paneIds) {
        if (paneIds.length === 1) {
            const pane = this.panes.get(paneIds[0]);
            return pane ? this.getPaneDisplayName(pane) : 'Pane';
        }
        return `Split (${paneIds.length})`;
    }

    getDefaultLayout(count) {
        switch (count) {
            case 1: return 'single';
            case 2: return 'horizontal';
            case 3: return 'grid';
            case 4: return 'grid';
            default: return 'grid';
        }
    }

    bindNewPaneSplits() {
        this.newPaneSplits.forEach(split => {
            const mainButton = split.querySelector('.new-pane-main');
            const toggleButton = split.querySelector('.new-pane-toggle');
            const menu = split.querySelector('.new-pane-menu');

            mainButton?.addEventListener('click', () => this.createNewPaneAndGroup('terminal'));
            toggleButton?.addEventListener('click', (e) => {
                e.stopPropagation();
                this.closeNewPaneMenus(split);
                this.togglePaneMenu(menu, toggleButton);
            });

            menu?.addEventListener('click', (e) => {
                const action = e.target.closest('.new-pane-action');
                if (action) {
                    e.stopPropagation();
                    this.closePaneMenu(menu, toggleButton);
                    this.restartBackend(action.dataset.backendId);
                    return;
                }
                const option = e.target.closest('.new-pane-option');
                if (!option || option.disabled || option.getAttribute('aria-disabled') === 'true') return;
                e.stopPropagation();
                this.closePaneMenu(menu, toggleButton);
                this.createNewPaneAndGroup(option.dataset.paneType || 'terminal');
            });
        });
    }

    renderPaneTypeMenus() {
        const paneTypes = this.paneTypes.filter(paneType => paneType.type !== 'terminal' || paneType.available !== false);
        const html = paneTypes.map(paneType => {
            const disabled = paneType.available === false;
            const statusMessage = disabled ? paneType.unavailableReason : paneType.warningReason;
            const versionMessage = paneType.version ? `Version: ${paneType.version}` : '';
            const titleMessage = [statusMessage, versionMessage].filter(Boolean).join(' ');
            const title = titleMessage ? ` title="${this.escapeHtml(titleMessage)}"` : '';
            const statusIcon = this.getPaneTypeStatusIconSvg(disabled ? 'error' : (paneType.warningReason ? 'warning' : ''), statusMessage);
            const option = `
                <button class="new-pane-option" data-pane-type="${this.escapeHtml(paneType.type)}" role="menuitem" ${disabled ? 'aria-disabled="true"' : ''}${title}>
                    ${this.getPaneTypeIconSvg(paneType.type, 18)}
                    <span class="new-pane-option-label">${this.escapeHtml(paneType.label || paneType.type)}</span>
                    ${statusIcon}
                </button>
            `;
            const canRestart = !disabled && paneType.backendScope === 'shared' && paneType.backendRunning === true;
            if (!canRestart) {
                return `<div class="new-pane-row">${option}</div>`;
            }
            const restartLabel = `Restart ${this.escapeHtml(paneType.label || paneType.type)} server`;
            return `
                <div class="new-pane-row">
                    ${option}
                    <button class="new-pane-action" data-backend-id="${this.escapeHtml(paneType.type)}" role="menuitem" title="${restartLabel}" aria-label="${restartLabel}">
                        <svg class="icon" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                            <path d="M20 12a8 8 0 1 1-2.34-5.66" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
                            <path d="M20 3v4h-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                    </button>
                </div>
            `;
        }).join('');

        this.newPaneSplits.forEach(split => {
            const menu = split.querySelector('.new-pane-menu');
            if (menu) menu.innerHTML = html;
        });
    }

    getPaneTypeStatusIconSvg(status, message = '') {
        if (!status) return '';
        const escapedMessage = this.escapeHtml(message || (status === 'error' ? 'Pane type unavailable' : 'Pane type warning'));
        if (status === 'error') {
            return `
                <span class="pane-type-status-icon pane-type-status-icon-error" title="${escapedMessage}" aria-label="${escapedMessage}">
                    <svg class="icon" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                        <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="2"/>
                        <path d="M12 7v6" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
                        <circle cx="12" cy="16.5" r="1" fill="currentColor"/>
                    </svg>
                </span>
            `;
        }
        return `
            <span class="pane-type-status-icon pane-type-status-icon-warning" title="${escapedMessage}" aria-label="${escapedMessage}">
                <svg class="icon" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                    <path d="M12 4l9 16H3L12 4z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/>
                    <path d="M12 9v5" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
                    <circle cx="12" cy="17" r="1" fill="currentColor"/>
                </svg>
            </span>
        `;
    }

    closeNewPaneMenus(exceptSplit = null) {
        this.newPaneSplits.forEach(split => {
            if (split === exceptSplit) return;
            this.closePaneMenu(split.querySelector('.new-pane-menu'), split.querySelector('.new-pane-toggle'));
        });
    }

    togglePaneMenu(menu, toggle) {
        if (!menu || !toggle) return;
        const isOpen = !menu.classList.contains('hidden');
        menu.classList.toggle('hidden', isOpen);
        toggle.setAttribute('aria-expanded', String(!isOpen));
        if (isOpen) return;
        menu.querySelector('.new-pane-option')?.focus();
    }

    closePaneMenu(menu, toggle) {
        menu?.classList.add('hidden');
        toggle?.setAttribute('aria-expanded', 'false');
    }

    async createNewPaneAndGroup(paneType = 'terminal') {
        if (!this.serverConnected) {
            this.toastError('Cannot create pane: server disconnected');
            return;
        }
        const pane = await this.createPane('', paneType);
        if (pane) {
            const existingGroup = Array.from(this.groups.values()).find(group => group.paneIds.includes(pane.id));
            if (existingGroup) {
                this.activateGroup(existingGroup.id);
                this.focusPane(pane.id);
                return;
            }
            const group = this.createGroup([pane.id]);
            this.addGroupToSidebar(group);
            this.refreshPaneTitles();
            this.activateGroup(group.id);
            this.saveUIState();
            requestAnimationFrame(() => {
                this.updatePaneLayout();
                this.focusPane(pane.id);
            });
        }
    }

    async createPane(name = '', type = 'terminal') {
        try {
            const response = await fetch(this.url('/api/panes'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name, type })
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to create pane');
            }

            const pane = await response.json();

            // Check for duplicate pane ID (shouldn't happen, but guard against it)
            if (this.panes.has(pane.id)) {
                return this.panes.get(pane.id);
            }

            // Mark when this pane was added to the frontend (for race condition protection)
            pane._addedAt = Date.now();
            this.panes.set(pane.id, pane);
            if (type === 'opencode') {
                this.loadServerInfo().catch(() => {});
            }
            return pane;
        } catch (error) {
            console.error('Failed to create pane:', error);
            this.toastError(`Failed to create pane: ${error.message}`);
            return null;
        }
    }

    async restartBackend(backendId) {
        if (!backendId) return;
        if (!this.serverConnected) {
            this.toastError('Cannot restart backend: server disconnected');
            return;
        }
        const label = this.paneTypes.find(type => type.type === backendId)?.label || backendId;
        if (!confirm(`Restart the ${label} server? Open ${label} panes will reload and any in-flight work in the backend will be interrupted.`)) {
            return;
        }

        // Flush pane storage first so UI state survives the iframe reload.
        const backendPane = Array.from(this.panes.values()).find(pane => pane.backendId === backendId);
        if (backendPane) {
            const popoutWindow = this.popoutWindows.get(this.getPopoutKey(backendPane));
            await this.flushOpenCodePaneStorage(backendPane, popoutWindow);
        }

        try {
            const response = await fetch(this.url(`/api/backends/${encodeURIComponent(backendId)}/restart`), { method: 'POST' });
            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to restart backend');
            }
            this.handleBackendRestarted(backendId);
        } catch (error) {
            console.error('Failed to restart backend:', error);
            this.toastError(`Failed to restart ${label} server: ${error.message}`);
            this.loadServerInfo().catch(() => {});
        }
    }

    handleBackendRestarted(backendId) {
        if (!backendId) return;
        const now = Date.now();
        if (now - (this.lastBackendRestart.get(backendId) || 0) < 2000) return;
        this.lastBackendRestart.set(backendId, now);

        let reloaded = false;
        for (const pane of this.panes.values()) {
            if (pane.backendId !== backendId || !this.isSharedPane(pane)) continue;
            const popoutWindow = this.popoutWindows.get(this.getPopoutKey(pane));
            if (popoutWindow && !popoutWindow.closed) {
                popoutWindow.location.href = this.url(`/p/${pane.id}/`);
            }
            if (!reloaded) {
                this.reloadSharedPaneIframe(pane);
                reloaded = true;
            }
        }
        if (reloaded) this.updatePaneLayout();
        this.loadServerInfo().catch(() => {});
    }

    async closePane(paneId, options = {}) {
        if ((!options.preAnimated && this.closingPaneIds.has(paneId)) || !this.panes.has(paneId)) return;
        this.closingPaneIds.add(paneId);
        const sidebarPositions = options.skipReflow ? null : this.getSidebarPanePositions();
        if (!options.preAnimated) await this.animatePaneClose(paneId);

        if (!this.panes.has(paneId)) return;

        for (const [groupId, group] of this.groups) {
            const paneIndexInGroup = group.paneIds.indexOf(paneId);
            if (paneIndexInGroup === -1) continue;

            // Find which pane this pane was in (for selecting next in visual order)
            const cm = group.cellMapping || group.paneIds.map((_, i) => i);
            const paneIndex = cm.indexOf(paneIndexInGroup);

            if (!this.removePaneFromGroup(group, paneId)) break;
            this.removePaneLocalState(paneId);

            if (group.paneIds.length === 0) {
                this.closeGroup(groupId, { skipPaneIds: new Set([paneId]) });
            } else {
                this.updateGroupLayout(group);
                this.updateGroupInSidebar(group);
                if (this.activeGroupId === groupId) {
                    this.updatePaneLayout();
                    // Focus the next pane in pane order, or previous if we closed the last
                    const newCm = group.cellMapping || group.paneIds.map((_, i) => i);
                    const nextPanePosition = Math.min(paneIndex, newCm.length - 1);
                    const nextPaneIndex = newCm[nextPanePosition];
                    if (nextPaneIndex !== undefined) {
                        this.focusPane(group.paneIds[nextPaneIndex]);
                    }
                }
            }
            break;
        }
        this.refreshPaneTitles();
        if (sidebarPositions) this.animateSidebarPaneReflow(sidebarPositions);

        try {
            const response = await fetch(this.url(`/api/panes/${paneId}`), { method: 'DELETE' });
            if (!response.ok && response.status !== 404) {
                console.error('Failed to close pane:', await response.text());
                this.closingPaneIds.delete(paneId);
            }
        } catch (error) {
            console.error('Failed to close pane:', error);
            this.closingPaneIds.delete(paneId);
        }
    }

    prefersReducedMotion() {
        return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    }

    getSidebarPanePositions() {
        const positions = new Map();
        this.paneList.querySelectorAll('.pane-item[data-pane-id]:not(.pane-closing)').forEach(item => {
            positions.set(item.dataset.paneId, item.getBoundingClientRect().top);
        });
        return positions;
    }

    async animatePaneClose(paneId) {
        if (this.prefersReducedMotion()) return;
        const sidebarItem = this.paneList.querySelector(`.pane-item[data-pane-id="${CSS.escape(paneId)}"]`);
        const paneContainer = document.getElementById(`pane-${paneId}`);
        const animatedElements = [sidebarItem, paneContainer].filter(Boolean);
        if (animatedElements.length === 0) return;

        animatedElements.forEach(element => element.classList.add('pane-closing'));
        await new Promise(resolve => setTimeout(resolve, 180));
    }

    animateSidebarPaneReflow(previousPositions) {
        if (this.prefersReducedMotion()) return;
        this.paneList.querySelectorAll('.pane-item[data-pane-id]').forEach(item => {
            const previousTop = previousPositions.get(item.dataset.paneId);
            if (previousTop === undefined) return;
            const delta = previousTop - item.getBoundingClientRect().top;
            if (Math.abs(delta) < 1) return;
            item.animate([
                { transform: `translateY(${delta}px)` },
                { transform: 'translateY(0)' },
            ], { duration: 180, easing: 'ease-out' });
        });
    }

    getPaneIdsInSidebarOrder(paneType = null) {
        return Array.from(this.paneList.querySelectorAll('.pane-item[data-pane-id]'))
            .map(item => item.dataset.paneId)
            .filter(paneId => !paneType || this.panes.get(paneId)?.type === paneType);
    }

    async closePanes(paneType = null) {
        const paneIds = this.getPaneIdsInSidebarOrder(paneType).reverse();
        await this.closePaneSequence(paneIds);
    }

    async closeGroupPanes(groupId) {
        const group = this.groups.get(groupId);
        if (!group) return;
        const paneIds = this.getPaneIdsInSidebarOrder().filter(paneId => group.paneIds.includes(paneId)).reverse();
        await this.closePaneSequence(paneIds);
    }

    async closePaneSequence(paneIds) {
        const closingIds = paneIds.filter(paneId => this.panes.has(paneId) && !this.closingPaneIds.has(paneId));
        if (closingIds.length === 0) return;

        const sidebarPositions = this.getSidebarPanePositions();
        const exitAnimations = [];
        for (const [index, paneId] of closingIds.entries()) {
            this.closingPaneIds.add(paneId);
            exitAnimations.push(this.animatePaneClose(paneId));
            if (!this.prefersReducedMotion() && index < closingIds.length - 1) {
                await new Promise(resolve => setTimeout(resolve, 40));
            }
        }

        await Promise.all(exitAnimations);
        const closePromises = closingIds.map(paneId => this.closePane(paneId, {
            preAnimated: true,
            skipReflow: true,
        }));
        this.animateSidebarPaneReflow(sidebarPositions);
        await Promise.all(closePromises);
    }

    setCloseAllConfirmation(button) {
        this.resetCloseAllConfirmation();
        this.pendingCloseAllButton = button;
        button.classList.add('close-all-confirm');
        button.textContent = `${button.dataset.label}?`;
        button.setAttribute('aria-label', `${button.dataset.label}? Click again to confirm`);
        this.closeAllWrapper.classList.add('confirming');
    }

    resetCloseAllConfirmation() {
        if (!this.pendingCloseAllButton) return;
        const button = this.pendingCloseAllButton;
        button.classList.remove('close-all-confirm');
        button.textContent = button.dataset.label;
        button.removeAttribute('aria-label');
        this.pendingCloseAllButton = null;
        this.closeAllWrapper.classList.remove('confirming');
    }

    dismissPaneMenus() {
        this.resetCloseAllConfirmation();
        this.closeNewPaneMenus();
        this.closeStorageActionMenus();
        this.closePaneActionMenus();
    }

    // Send input to a pane via the pane input API
    // payload can be:
    //   - Simple: { keys: ['C-c', 'C-d'] }
    //   - Extended: { sequence: [{type: 'key', value: 'C-c'}, {type: 'text', value: 'hello\n'}] }
    async sendInputToPane(paneId, payload) {
        const pane = this.panes.get(paneId);
        if (!this.paneSupportsKeybarInput(pane)) {
            return false;
        }

        try {
            const response = await fetch(this.url(`/api/panes/${paneId}/input`), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to send input');
            }

            return true;
        } catch (error) {
            console.error('Failed to send input:', error);
            return false;
        }
    }

    // Send input to the currently active pane
    async sendInputToActivePane(payload) {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.paneIds.length === 0) {
            console.warn('No active pane to send input to');
            return false;
        }

        // Send to the focused pane within the group, falling back to the first
        const activePaneId = (this.focusedPaneId && activeGroup.paneIds.includes(this.focusedPaneId))
            ? this.focusedPaneId
            : activeGroup.paneIds[0];
        return this.sendInputToPane(activePaneId, payload);
    }

    closeGroup(groupId, options = {}) {
        const group = this.groups.get(groupId);
        if (!group) return;

        // Find the index of this group before removing it
        const groupIndex = this.groupOrder.indexOf(groupId);

        for (const paneId of [...group.paneIds]) {
            if (!options.skipPaneIds?.has(paneId)) {
                this.closingPaneIds.add(paneId);
                fetch(this.url(`/api/panes/${paneId}`), { method: 'DELETE' })
                    .then(response => {
                        if (!response.ok && response.status !== 404) {
                            this.closingPaneIds.delete(paneId);
                        }
                    })
                    .catch(() => this.closingPaneIds.delete(paneId));
            }
            this.removePaneLocalState(paneId);
        }

        this.groups.delete(groupId);
        this.groupOrder = this.groupOrder.filter(id => id !== groupId);
        this.forgetGroupSelection(groupId);
        document.getElementById(`group-${groupId}`)?.remove();

        if (this.activeGroupId === groupId) {
            this.activeGroupId = null;
            const previousSelection = this.getPreviousGroupSelection();
            // Fall back to sidebar order when there is no selection history.
            const fallbackIndex = Math.min(groupIndex, this.groupOrder.length - 1);
            const fallbackGroupId = this.groupOrder[fallbackIndex];
            if (previousSelection) {
                this.activateGroup(previousSelection.groupId, previousSelection.paneId);
            } else if (fallbackGroupId) {
                this.activateGroup(fallbackGroupId);
            } else {
                this.focusedPaneId = null;
                this.updatePaneLayout();
                this.noPaneEl.classList.remove('hidden');
                this.keybar.classList.add('hidden');
                this.keybarToggle.classList.remove('active');
                this.updateMobileKeybarVisibility();
                this.clearIconFade?.();
            }
        }

        this.refreshPaneTitles();
        this.saveUIState();
    }

    removePaneLocalState(paneId) {
        const pane = this.panes.get(paneId);
        if (!pane) return null;

        const popoutKey = this.getPopoutKey(pane);
        this.attentionPaneIds.delete(paneId);
        this.updateAttentionTitle();
        this.panes.delete(paneId);

        const hasRemainingMirror = this.isSharedPane(pane) && Array.from(this.panes.values()).some(p => p.backendId === pane.backendId);
        this.cleanupPanePopout(popoutKey, hasRemainingMirror);
        this.removePaneContainer(paneId);

        return pane;
    }

    cleanupPanePopout(popoutKey, keepAlive = false) {
        if (!popoutKey || keepAlive) return;

        const popoutWindow = this.popoutWindows.get(popoutKey);
        if (popoutWindow && !popoutWindow.closed) {
            popoutWindow.close();
        }
        this.clearPopoutTracking(popoutKey);
    }

    breakOutPane(paneId, groupId) {
        const group = this.groups.get(groupId);
        if (!group || group.paneIds.length <= 1) return;

        if (!this.removePaneFromGroup(group, paneId)) return;

        this.updateGroupLayout(group);
        this.updateGroupInSidebar(group);

        const newGroup = this.createGroup([paneId]);
        this.addGroupToSidebar(newGroup);

        this.refreshPaneTitles();
        this.activateGroup(newGroup.id);
    }

    breakOutAllPanes(groupId) {
        const group = this.groups.get(groupId);
        if (!group || group.paneIds.length <= 1) return;

        const paneIds = this.getGroupPaneIdsInVisualOrder(group);
        const groupIndex = this.groupOrder.indexOf(groupId);

        this.groups.delete(groupId);
        this.groupOrder = this.groupOrder.filter(id => id !== groupId);
        this.forgetGroupSelection(groupId);
        document.getElementById(`group-${groupId}`)?.remove();

        let firstNewGroup = null;
        const newGroupIds = [];
        for (const paneId of paneIds) {
            const newGroup = this.createGroup([paneId]);
            this.addGroupToSidebar(newGroup);
            newGroupIds.push(newGroup.id);
            if (!firstNewGroup) firstNewGroup = newGroup;
        }

        this.groupOrder = this.groupOrder.filter(id => !newGroupIds.includes(id));
        this.groupOrder.splice(groupIndex, 0, ...newGroupIds);
        this.rerenderSidebarOrder();

        if (this.activeGroupId === groupId && firstNewGroup) {
            this.activeGroupId = null;
            this.activateGroup(firstNewGroup.id);
        }
        this.refreshPaneTitles();
    }

    // SECTION: SIDEBAR

    // Sidebar UI
    // ==========

    paneAttentionEnabled(pane) {
        const panes = this.settings?.panes;
        if (!pane || panes?.attentionIndicators !== true) return false;
        return panes[pane.type]?.indicateAttention === true;
    }

    markPaneAttention(paneId, force = false, attentionEvent = '') {
        const pane = this.panes.get(paneId);
        if (!this.paneAttentionEnabled(pane)) return;
        if (!force && this.focusedPaneId === paneId && !document.hidden && document.hasFocus()) return;
        const alreadyMarked = this.attentionPaneIds.has(paneId);
        const needsSound = attentionEvent
            ? !this.knownAudibleAttentionEvents.has(attentionEvent)
            : pane.type === 'terminal' && !alreadyMarked;
        if (needsSound && this.settings?.panes?.playAttentionSound === true) {
            this.playAttentionSound();
        }
        if (alreadyMarked) return;

        this.attentionPaneIds.add(paneId);
        this.updatePaneAttentionInSidebar(paneId);
        this.updateAttentionTitle();
        this.saveUIState();
    }

    clearPaneAttention(paneId) {
        if (!this.attentionPaneIds.delete(paneId)) return;
        this.updatePaneAttentionInSidebar(paneId);
        this.updateAttentionTitle();
        this.saveUIState();
    }

    clearFocusedPaneAttention() {
        if (this.focusedPaneId && !document.hidden) this.clearPaneAttentionOnFocus(this.focusedPaneId);
    }

    clearPaneAttentionOnFocus(paneId) {
        if (this.panes.get(paneId)?.type !== 'opencode') this.clearPaneAttention(paneId);
    }

    updatePaneAttentionInSidebar(paneId) {
        for (const group of this.groups.values()) {
            if (group.paneIds.includes(paneId)) this.updateGroupInSidebar(group);
        }
    }

    updateAttentionTitle() {
        const showInTitle = this.settings?.panes?.attentionIndicators === true
            && this.settings?.panes?.showAttentionInTitle === true;
        document.title = showInTitle && this.attentionPaneIds.size > 0
            ? `[${this.attentionPaneIds.size}] ${this.baseDocumentTitle}`
            : this.baseDocumentTitle;
    }

    createAttentionSoundBlob() {
        const sampleRate = 44100;
        const sampleCount = Math.ceil(sampleRate * 0.36);
        const buffer = new ArrayBuffer(44 + sampleCount * 2);
        const view = new DataView(buffer);
        const writeText = (offset, value) => {
            for (let index = 0; index < value.length; index++) {
                view.setUint8(offset + index, value.charCodeAt(index));
            }
        };

        writeText(0, 'RIFF');
        view.setUint32(4, 36 + sampleCount * 2, true);
        writeText(8, 'WAVEfmt ');
        view.setUint32(16, 16, true);
        view.setUint16(20, 1, true);
        view.setUint16(22, 1, true);
        view.setUint32(24, sampleRate, true);
        view.setUint32(28, sampleRate * 2, true);
        view.setUint16(32, 2, true);
        view.setUint16(34, 16, true);
        writeText(36, 'data');
        view.setUint32(40, sampleCount * 2, true);

        const tone = (time, frequency, offset) => {
            const localTime = time - offset;
            if (localTime < 0 || localTime > 0.24) return 0;
            const gain = localTime < 0.015
                ? 0.0001 * Math.pow(10000, localTime / 0.015)
                : Math.pow(0.0001, (localTime - 0.015) / 0.225);
            return Math.sin(2 * Math.PI * frequency * localTime) * gain;
        };
        for (let index = 0; index < sampleCount; index++) {
            const time = index / sampleRate;
            const sample = tone(time, 659.25, 0) + tone(time, 880, 0.11);
            view.setInt16(44 + index * 2, Math.round(Math.max(-1, Math.min(1, sample)) * 32767), true);
        }
        return new Blob([buffer], { type: 'audio/wav' });
    }

    playAttentionSound(preview = false) {
        if (!preview && this.settings?.panes?.playAttentionSound !== true) return;
        if (navigator.userActivation?.hasBeenActive === false) return;

        this.attentionSoundBlob ||= this.createAttentionSoundBlob();
        const objectURL = URL.createObjectURL(this.attentionSoundBlob);
        const audio = new Audio(objectURL);
        this.activeAttentionSounds.add(audio);
        const cleanup = () => {
            if (!this.activeAttentionSounds.delete(audio)) return;
            audio.removeAttribute('src');
            audio.load();
            URL.revokeObjectURL(objectURL);
        };
        audio.addEventListener('ended', cleanup, { once: true });
        audio.addEventListener('error', cleanup, { once: true });
        audio.play().catch(cleanup);
    }

    reconcileAttentionSettings() {
        const changedPaneIds = [];
        for (const paneId of this.attentionPaneIds) {
            if (this.paneAttentionEnabled(this.panes.get(paneId))) continue;
            this.attentionPaneIds.delete(paneId);
            changedPaneIds.push(paneId);
        }
        changedPaneIds.forEach(paneId => this.updatePaneAttentionInSidebar(paneId));
        this.updateAttentionTitle();
        if (changedPaneIds.length > 0) this.saveUIState();
    }

    renderPaneAttention(pane) {
        if (!this.attentionPaneIds.has(pane?.id)) return '<span class="pane-attention-slot" aria-hidden="true"></span>';
        return `<span class="pane-attention-slot" title="Attention requested" aria-hidden="true">
            <svg class="pane-attention-icon" viewBox="0 0 24 24" width="16" height="16">
                <path fill="currentColor" d="M12 22a2.25 2.25 0 0 0 2.12-1.5H9.88A2.25 2.25 0 0 0 12 22Zm7-5.25-1.75-1.75v-4.5a5.26 5.26 0 0 0-4-5.1V4.5a1.25 1.25 0 0 0-2.5 0v.9a5.26 5.26 0 0 0-4 5.1V15L5 16.75V18h14v-1.25ZM7.41 16.5l.84-.84V10.5a3.75 3.75 0 0 1 7.5 0v5.16l.84.84H7.41Z"/>
            </svg>
        </span>`;
    }

    handlePaneAttentionMessage(msg, source = null) {
        if (!msg || msg.type !== 'webmux-pane-attention') return;
        let paneId = msg.paneId;
        const iframe = source
            ? Array.from(this.sharedIframes.values()).find(candidate => candidate.contentWindow === source)
            : (msg.backendId ? this.sharedIframes.get(msg.backendId) : null);
        if (iframe?.dataset.activePaneId) paneId = iframe.dataset.activePaneId;
        if (msg.active === false) this.clearPaneAttention(paneId);
        else this.markPaneAttention(paneId, msg.force === true, msg.attentionEvent);
    }

    addGroupToSidebar(group) {
        const container = document.createElement('div');
        container.id = `group-${group.id}`;
        container.className = 'group-container';
        container.innerHTML = this.renderGroupSidebarHTML(group);

        this.bindGroupEvents(container, group);
        this.paneList.appendChild(container);
    }

    updateGroupInSidebar(group) {
        const container = document.getElementById(`group-${group.id}`);
        if (!container) return;

        // Don't re-render if we're in the middle of renaming
        if (this.renamingPaneId && group.paneIds.includes(this.renamingPaneId)) {
            return;
        }

        container.innerHTML = this.renderGroupSidebarHTML(group);
        this.bindGroupEvents(container, group);
    }

    renderGroupSidebarHTML(group) {
        // Filter out any pane IDs that no longer exist
        const validPaneIds = group.paneIds.filter(id => this.panes.has(id));
        if (validPaneIds.length !== group.paneIds.length) {
            // Update group with only valid panes
            group.paneIds = validPaneIds;
            // Reset cellMapping since we can't easily remap after arbitrary removals
            group.cellMapping = null;
            if (validPaneIds.length === 0) {
                // Schedule group removal (can't do it during render)
                setTimeout(() => this.closeGroup(group.id), 0);
                return '<div class="pane-item">Closing...</div>';
            }
            this.updateGroupLayout(group);
        }

        const isMulti = validPaneIds.length > 1;
        const activePaneInGroup = this.activeGroupId === group.id && validPaneIds.includes(this.focusedPaneId);

        if (!isMulti) {
            const pane = this.panes.get(validPaneIds[0]);
            const displayName = this.getPaneDisplayName(pane);
            const activity = this.getPaneActivityDisplay(pane);
            const activityHtml = activity ? `<span class="activity-name"> · ${this.escapeHtml(activity)}</span>` : '';
            const isRenaming = this.renamingPaneId === pane?.id;
            const paneTypeLabel = this.getPaneTypeLabel(pane);
            const hasAttention = this.attentionPaneIds.has(pane?.id);

            const nameHtml = isRenaming
                ? `<input type="text" class="inline-rename-input" value="${this.escapeHtmlAttribute(pane?.name || '')}" placeholder="${this.escapeHtmlAttribute(displayName)}" data-pane-id="${this.escapeHtmlAttribute(pane?.id || '')}">`
                : `<span class="name"><span class="pane-name-text">${this.escapeHtml(displayName)}</span>${activityHtml}</span>`;

            return `
                <div class="pane-item ${activePaneInGroup ? 'active' : ''} ${hasAttention ? 'has-attention' : ''}"
                     data-group-id="${group.id}" data-pane-id="${pane?.id}" draggable="${!isRenaming}"
                     role="button" aria-label="${paneTypeLabel} pane: ${this.escapeHtmlAttribute(displayName)}${hasAttention ? ', attention requested' : ''}">
                    ${this.getPaneIconSvg(pane, 18)}
                    ${nameHtml}
                    ${this.renderPaneAttention(pane)}
                    ${this.renderPaneActions(pane, group.id, { closeGroup: true, active: activePaneInGroup })}
                </div>
            `;
        }

        const paneItems = this.getGroupPaneIdsInVisualOrder(group).map(sid => {
            const pane = this.panes.get(sid);
            const displayName = this.getPaneDisplayName(pane);
            const activity = this.getPaneActivityDisplay(pane);
            const activityHtml = activity ? `<span class="activity-name"> · ${this.escapeHtml(activity)}</span>` : '';
            const paneTypeLabel = this.getPaneTypeLabel(pane);
            const isActivePane = activePaneInGroup && this.focusedPaneId === sid;
            const hasAttention = this.attentionPaneIds.has(sid);
            const isRenaming = this.renamingPaneId === sid;
            const nameHtml = isRenaming
                ? `<input type="text" class="inline-rename-input" value="${this.escapeHtmlAttribute(pane?.name || '')}" placeholder="${this.escapeHtmlAttribute(displayName)}" data-pane-id="${this.escapeHtmlAttribute(sid)}">`
                : `<span class="name"><span class="pane-name-text">${this.escapeHtml(displayName)}</span>${activityHtml}</span>`;
            return `
                <div class="pane-item sub-item ${isActivePane ? 'active' : ''} ${hasAttention ? 'has-attention' : ''}" data-pane-id="${sid}" data-group-id="${group.id}" draggable="${!isRenaming}"
                     role="button" aria-label="${paneTypeLabel} pane: ${this.escapeHtmlAttribute(displayName)}${hasAttention ? ', attention requested' : ''}">
                    ${this.getPaneIconSvg(pane, 16)}
                    ${nameHtml}
                    ${this.renderPaneAttention(pane)}
                    ${this.renderPaneActions(pane, group.id, { breakout: true, active: isActivePane })}
                </div>
            `;
        }).join('');

        return `
            <div class="group-header compact ${activePaneInGroup ? 'active' : ''}" data-group-id="${group.id}" draggable="true"
                 role="button" aria-label="Pane group: ${this.escapeHtmlAttribute(this.getGroupDisplayName(group))}">
                <svg class="icon" viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                    <path fill="currentColor" d="M3 5v14h18V5H3zm8 12H5v-5h6v5zm0-7H5V5h6v5zm8 7h-6v-5h6v5zm0-7h-6V5h6v5z"/>
                </svg>
                <span class="name">Split</span>
                <div class="actions">
                    <button class="action-btn breakout-all" title="Break out all" data-group-id="${group.id}" aria-label="Break out all panes">
                        <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
                            <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                        </svg>
                    </button>
                    <button class="action-btn close" title="Close all" data-group-id="${group.id}" aria-label="Close all panes in group">
                        <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
                            <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                        </svg>
                    </button>
                </div>
            </div>
            <div class="group-panes">
                ${paneItems}
            </div>
        `;
    }

    renderPaneActions(pane, groupId, options = {}) {
        const paneId = pane?.id || '';
        const paneTypeLabel = this.getPaneTypeLabel(pane);
        const closeAttr = options.closeGroup
            ? `data-group-id="${this.escapeHtml(groupId)}"`
            : `data-pane-id="${this.escapeHtml(paneId)}"`;
        const breakoutItem = options.breakout ? `
            <button class="pane-menu-item" data-action="breakout" data-pane-id="${this.escapeHtml(paneId)}" data-group-id="${this.escapeHtml(groupId)}" role="menuitem">
                <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                    <path fill="currentColor" d="M4 4h7V2H4a2 2 0 0 0-2 2v7h2V4zm6 12l-4 4h3v2H2v-7h2v3l4-4 2 2zm8-6l4-4v3h2V2h-7v2h3l-4 4 2 2z"/>
                </svg>
                Break out
            </button>
        ` : '';
        const popoutMenuItem = options.active ? '' : `
            <button class="pane-menu-item" data-action="popout" data-pane-id="${this.escapeHtml(paneId)}" role="menuitem">
                <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                    <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                </svg>
                Pop out
            </button>
        `;

        return `
            <div class="actions">
                <div class="pane-action-menu-wrapper">
                    <button class="action-btn pane-menu-toggle" title="More actions" data-pane-id="${this.escapeHtml(paneId)}" aria-label="More actions for ${paneTypeLabel} pane" aria-haspopup="menu" aria-expanded="false">
                        <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                            <path fill="currentColor" d="M12 8c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2zm0 2c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm0 6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2z"/>
                        </svg>
                    </button>
                    <div class="pane-action-menu hidden" role="menu">
                        <button class="pane-menu-item" data-action="refresh" data-pane-id="${this.escapeHtml(paneId)}" role="menuitem">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M17.65 6.35A7.95 7.95 0 0 0 12 4a8 8 0 1 0 7.45 5.08h-2.12A6 6 0 1 1 12 6c1.66 0 3.14.69 4.22 1.78L13 11h8V3l-3.35 3.35z"/>
                            </svg>
                            Refresh
                        </button>
                        ${popoutMenuItem}
                        <button class="pane-menu-item" data-action="rename" data-pane-id="${this.escapeHtml(paneId)}" role="menuitem">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M20.71 7.04c.39-.39.39-1.04 0-1.41l-2.34-2.34c-.37-.39-1.02-.39-1.41 0l-1.84 1.83 3.75 3.75M3 17.25V21h3.75L17.81 9.93l-3.75-3.75L3 17.25z"/>
                            </svg>
                            Rename
                        </button>
                        ${breakoutItem}
                    </div>
                </div>
                <button class="action-btn popout" title="Pop out" data-pane-id="${this.escapeHtml(paneId)}" aria-label="Pop out ${paneTypeLabel} pane">
                    <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                        <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                    </svg>
                </button>
                <button class="action-btn close" title="Close" ${closeAttr} aria-label="Close ${paneTypeLabel} pane">
                    <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                        <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                    </svg>
                </button>
            </div>
        `;
    }

    bindGroupEvents(container, group) {
        const header = container.querySelector('.group-header');
        if (header) {
            header.addEventListener('click', (e) => {
                if (!e.target.closest('.actions')) {
                    // Focus first pane when clicking group header
                    this.activateGroup(group.id, this.getGroupPaneIdsInVisualOrder(group)[0]);
                }
            });
        }

        const singleItem = container.querySelector('.pane-item:not(.sub-item)');
        if (singleItem && !header) {
            singleItem.addEventListener('click', (e) => {
                if (!e.target.closest('.actions') && !e.target.closest('.inline-rename-input')) {
                    const paneId = singleItem.dataset.paneId;
                    this.activateGroup(group.id, paneId);
                }
            });
        }

        container.querySelectorAll('.pane-item.sub-item').forEach(item => {
            item.addEventListener('click', (e) => {
                if (!e.target.closest('.actions') && !e.target.closest('.inline-rename-input')) {
                    const paneId = item.dataset.paneId;
                    this.activateGroup(group.id, paneId);
                }
            });

            item.addEventListener('mouseenter', () => {
                const paneId = item.dataset.paneId;
                this.highlightPaneInGroup(paneId, true);
            });
            item.addEventListener('mouseleave', () => {
                const paneId = item.dataset.paneId;
                this.highlightPaneInGroup(paneId, false);
            });
        });

        container.querySelectorAll('[draggable="true"]').forEach(item => {
            item.addEventListener('dragstart', (e) => {
                this.draggedPaneId = item.dataset.paneId;
                this.draggedGroupId = item.dataset.groupId;
                item.classList.add('dragging');
                e.dataTransfer.effectAllowed = 'move';
                e.dataTransfer.setData('text/plain', this.draggedPaneId || this.draggedGroupId);
            });
            item.addEventListener('dragend', () => {
                item.classList.remove('dragging');
                this.draggedPaneId = null;
                this.draggedGroupId = null;
                this.hideDragOverlay();
                this.clearSidebarDropIndicators();
            });
        });

        container.addEventListener('dragover', (e) => {
            if (!this.draggedPaneId && !this.draggedGroupId) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            this.updateSidebarDropIndicator(container, e);
        });

        container.addEventListener('dragleave', (e) => {
            if (!container.contains(e.relatedTarget)) {
                container.classList.remove('drop-above', 'drop-below');
            }
        });

        container.addEventListener('drop', (e) => {
            e.preventDefault();
            this.handleSidebarDrop(container, group.id, e);
        });

        container.querySelectorAll('.action-btn.close').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                const paneId = btn.dataset.paneId;
                const groupId = btn.dataset.groupId;
                if (paneId) {
                    this.closePane(paneId);
                } else if (groupId) {
                    this.closeGroupPanes(groupId);
                }
            });
        });

        container.querySelectorAll('.pane-menu-toggle').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                const menu = btn.closest('.pane-action-menu-wrapper')?.querySelector('.pane-action-menu');
                const isOpen = menu && !menu.classList.contains('hidden');
                this.closePaneActionMenus(menu);
                menu?.classList.toggle('hidden', isOpen);
                btn.setAttribute('aria-expanded', String(!isOpen));
                btn.closest('.pane-item')?.classList.toggle('menu-open', !isOpen);
            });
        });

        container.querySelectorAll('.pane-menu-item').forEach(item => {
            item.addEventListener('click', (e) => {
                e.stopPropagation();
                const paneId = item.dataset.paneId;
                const action = item.dataset.action;
                this.closePaneActionMenus();
                if (action === 'refresh') {
                    this.refreshPane(paneId);
                } else if (action === 'popout') {
                    this.popOutPane(paneId);
                } else if (action === 'rename') {
                    this.startInlineRename(paneId);
                } else if (action === 'breakout') {
                    this.breakOutPane(paneId, item.dataset.groupId);
                }
            });
        });

        container.querySelectorAll('.action-btn.rename').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.startInlineRename(btn.dataset.paneId);
            });
        });

        container.querySelectorAll('.action-btn.breakout').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.breakOutPane(btn.dataset.paneId, btn.dataset.groupId);
            });
        });

        container.querySelectorAll('.action-btn.breakout-all').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.breakOutAllPanes(btn.dataset.groupId);
            });
        });

        container.querySelectorAll('.action-btn.popout').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.popOutPane(btn.dataset.paneId);
            });
        });

        // Handle inline rename input
        const renameInput = container.querySelector('.inline-rename-input');
        if (renameInput) {
            renameInput.focus();
            renameInput.select();

            renameInput.addEventListener('keydown', (e) => {
                if (e.key === 'Enter') {
                    e.preventDefault();
                    this.finishInlineRename(renameInput.value);
                } else if (e.key === 'Escape') {
                    e.preventDefault();
                    this.cancelInlineRename();
                }
                e.stopPropagation();
            });

            renameInput.addEventListener('blur', () => {
                // Small delay to allow click events to process first
                setTimeout(() => {
                    if (this.renamingPaneId) {
                        this.finishInlineRename(renameInput.value);
                    }
                }, 100);
            });

            renameInput.addEventListener('click', (e) => {
                e.stopPropagation();
            });
        }
    }

    closePaneActionMenus(exceptMenu = null) {
        document.querySelectorAll('.pane-action-menu').forEach(menu => {
            if (menu === exceptMenu) return;
            menu.classList.add('hidden');
            menu.closest('.pane-action-menu-wrapper')?.querySelector('.pane-menu-toggle')?.setAttribute('aria-expanded', 'false');
            menu.closest('.pane-item')?.classList.remove('menu-open');
        });
    }

    getPaneDisplayName(pane) {
        if (!pane) return 'Pane';
        if (typeof pane.name === 'string' && pane.name.length > 0) return pane.name;
        const position = this.getPaneIdsInCanonicalOrder().indexOf(pane.id);
        return String(position >= 0 ? position + 1 : this.panes.size + 1);
    }

    getPaneTypeLabel(pane) {
        if (pane?.type === 'opencode') return 'OpenCode';
        return 'Terminal';
    }

    getPaneIconSvg(pane, size = 18) {
        return this.getPaneTypeIconSvg(pane?.type || 'terminal', size);
    }

    getPaneTypeIconSvg(paneType, size = 18) {
        if (paneType === 'opencode') {
            return `
                <svg class="icon pane-type-icon pane-type-icon-opencode" viewBox="0 0 300 300" width="${size}" height="${size}" aria-hidden="true">
                    <path d="M210 240H90V120h120v120Z" fill="currentColor" opacity="0.45"/>
                    <path d="M210 60H90v180h120V60Zm60 240H30V0h240v300Z" fill="currentColor"/>
                </svg>
            `;
        }

        return `
            <svg class="icon pane-type-icon pane-type-icon-terminal" viewBox="0 0 24 24" width="${size}" height="${size}" aria-hidden="true">
                <path fill="currentColor" d="M20 19V7H4v12h16m0-16a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h16m-7 14v-2h5v2h-5m-3.42-4L5.57 9H8.4l3.3 3.3c.39.39.39 1.03 0 1.42L8.42 17H5.59l4-4z"/>
            </svg>
        `;
    }

    getPaneActivityDisplay(pane) {
        if (this.isSharedPane(pane)) return this.getPaneTypeLabel(pane);
        if (!pane || !pane.currentActivity) return '';
        return pane.currentActivity;
    }

    highlightPaneInGroup(paneId, highlight) {
        const container = document.getElementById(`pane-${paneId}`);
        if (container) {
            container.classList.toggle('highlighted', highlight);
        }
    }

    clearAllHighlights() {
        document.querySelectorAll('.pane-container.highlighted').forEach(el => {
            el.classList.remove('highlighted');
        });
    }

    // Inline Rename
    // =============

    startInlineRename(paneId) {
        const pane = this.panes.get(paneId);
        if (!pane) return;

        this.renamingPaneId = paneId;

        // Find the group containing this pane and re-render
        for (const group of this.groups.values()) {
            if (group.paneIds.includes(paneId)) {
                const container = document.getElementById(`group-${group.id}`);
                if (container) {
                    container.innerHTML = this.renderGroupSidebarHTML(group);
                    this.bindGroupEvents(container, group);
                }
                break;
            }
        }
    }

    async finishInlineRename(newName) {
        if (!this.renamingPaneId) return;

        const paneId = this.renamingPaneId;
        const normalizedName = newName.trim();
        this.renamingPaneId = null;

        try {
            const response = await fetch(this.url(`/api/panes/${paneId}`), {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name: normalizedName })
            });
            if (!response.ok) throw new Error(`HTTP ${response.status}`);

            const pane = this.panes.get(paneId);
            if (pane) pane.name = normalizedName;
        } catch (error) {
            console.error('Failed to rename pane:', error);
        }

        this.refreshPaneTitles();
    }

    cancelInlineRename() {
        if (!this.renamingPaneId) return;

        const paneId = this.renamingPaneId;
        this.renamingPaneId = null;

        // Re-render without the input
        for (const group of this.groups.values()) {
            if (group.paneIds.includes(paneId)) {
                this.updateGroupInSidebar(group);
                break;
            }
        }
    }

    // Popout Management
    // =================

    setupPopoutRegistry() {
        this.loadPopoutStates();

        if ('BroadcastChannel' in window) {
            this.popoutChannel = new BroadcastChannel('webmux-popouts');
            this.popoutChannel.onmessage = (event) => this.handlePopoutMessage(event.data);
            this.popoutChannel.postMessage({ type: 'webmux-popout-discover' });
        }

        setInterval(() => this.pruneStalePopouts(), 1000);
    }

    loadPopoutStates() {
        try {
            const raw = localStorage.getItem(this.popoutStorageKey);
            if (!raw) return;
            const entries = JSON.parse(raw);
            if (!Array.isArray(entries)) return;
            const now = Date.now();
            for (const entry of entries) {
                if (!entry?.key || now - (entry.lastSeen || 0) > this.popoutStaleMs) continue;
                this.popoutStates.set(entry.key, entry);
            }
        } catch (error) {
            console.warn('Failed to load popout state:', error);
        }
    }

    savePopoutStates() {
        try {
            localStorage.setItem(this.popoutStorageKey, JSON.stringify(Array.from(this.popoutStates.values())));
        } catch (error) {
            console.warn('Failed to save popout state:', error);
        }
    }

    handlePopoutMessage(msg) {
        if (!msg || !msg.type || !msg.paneId) return;

        if (msg.type === 'webmux-pane-attention') {
            this.handlePaneAttentionMessage(msg);
        } else if (msg.type === 'webmux-popout-alive') {
            this.registerPopoutAlive(msg);
        } else if (msg.type === 'webmux-popout-closed') {
            this.registerPopoutClosed(msg);
        } else if (msg.type === 'webmux-keybar-state-request') {
            this.popoutChannel?.postMessage({
                type: 'webmux-keybar-visibility',
                paneId: msg.paneId,
                hidden: this.keybarUserHidden,
            });
        }
    }

    broadcastKeybarVisibility() {
        this.popoutChannel?.postMessage({
            type: 'webmux-keybar-visibility',
            paneId: this.focusedPaneId || '*',
            hidden: this.keybarUserHidden,
            applyToAll: true,
        });
    }

    registerPopoutAlive(msg) {
        const pane = this.panes.get(msg.paneId);
        if (!pane) {
            this.pendingPopoutAlives.set(msg.paneId, msg);
            return;
        }

        if (!this.isWebmuxOpenedPopout(msg)) {
            this.broadcastPaneOwnerMain(msg.paneId, msg.popoutId, msg.windowName);
            return;
        }

        const key = this.getPopoutKey(pane);
        if (!key) return;

        const suppressUntil = this.popoutSuppressUntil.get(key) || 0;
        if (Date.now() < suppressUntil) return;
        this.popoutSuppressUntil.delete(key);

        this.cancelPendingPopoutClose(key, msg.popoutId);

        const wasPoppedOut = this.popoutStates.has(key);
        this.popoutStates.set(key, {
            key,
            paneId: msg.paneId,
            popoutId: msg.popoutId,
            backendId: pane.backendId,
            backendScope: pane.backendScope,
            href: msg.href,
            lastSeen: msg.lastSeen || Date.now(),
        });
        this.savePopoutStates();

        this.setPoppedOutContainers(key);

        if (!wasPoppedOut) {
            this.updatePaneLayout();
        }
    }

    registerPopoutClosed(msg) {
        const pane = this.panes.get(msg.paneId);
        const key = pane ? this.getPopoutKey(pane) : this.findPopoutKeyByMessage(msg);
        if (!key) return;

        const state = this.popoutStates.get(key);
        if (state?.popoutId && msg.popoutId && state.popoutId !== msg.popoutId) return;

        this.cancelPendingPopoutClose(key);
        const timer = setTimeout(() => {
            const latestState = this.popoutStates.get(key);
            if (latestState?.popoutId && msg.popoutId && latestState.popoutId !== msg.popoutId) return;
            this.pendingPopoutCloses.delete(key);
            this.clearPopoutTracking(key);
            this.clearPoppedOutContainers(key);
            this.updatePaneLayout();
        }, 1200);
        this.pendingPopoutCloses.set(key, { timer, popoutId: msg.popoutId });
    }

    isWebmuxOpenedPopout(msg) {
        return typeof msg.windowName === 'string' && msg.windowName.startsWith('webmux-');
    }

    broadcastPaneOwnerMain(paneId, popoutId = null, windowName = null) {
        if (!paneId) return;
        this.popoutChannel?.postMessage({
            type: 'webmux-pane-owner-main',
            paneId,
            popoutId,
            windowName,
        });
    }

    cancelPendingPopoutClose(key, popoutId = null) {
        const pending = this.pendingPopoutCloses.get(key);
        if (!pending) return;
        if (popoutId && pending.popoutId && pending.popoutId !== popoutId) return;
        clearTimeout(pending.timer);
        this.pendingPopoutCloses.delete(key);
    }

    findPopoutKeyByMessage(msg) {
        for (const [key, state] of this.popoutStates) {
            if ((msg.popoutId && state.popoutId === msg.popoutId) || state.paneId === msg.paneId) {
                return key;
            }
        }
        return null;
    }

    reconcilePopoutStates() {
        for (const msg of this.pendingPopoutAlives.values()) {
            this.registerPopoutAlive(msg);
        }
        this.pendingPopoutAlives.clear();

        for (const [key, state] of Array.from(this.popoutStates)) {
            if (!this.popoutStateHasPane(key, state)) {
                this.popoutStates.delete(key);
            }
        }
        this.savePopoutStates();
    }

    popoutStateHasPane(key, state) {
        if (key.startsWith('pane:')) {
            return this.panes.has(state.paneId);
        }
        if (key.startsWith('backend:')) {
            return Array.from(this.panes.values()).some(pane => pane.backendId === state.backendId);
        }
        return false;
    }

    pruneStalePopouts() {
        const now = Date.now();
        let changed = false;
        for (const [key, until] of Array.from(this.popoutSuppressUntil)) {
            if (now >= until) this.popoutSuppressUntil.delete(key);
        }
        for (const [key, state] of Array.from(this.popoutStates)) {
            if (now - (state.lastSeen || 0) <= this.popoutStaleMs) continue;
            this.clearPopoutTracking(key);
            this.clearPoppedOutContainers(key);
            changed = true;
        }
        if (changed) {
            this.savePopoutStates();
            this.updatePaneLayout();
        }
    }

    getPopoutKey(pane) {
        if (!pane) return null;
        return this.isSharedPane(pane) ? `backend:${pane.backendId}` : `pane:${pane.id}`;
    }

    getPopoutKeyForPaneId(paneId) {
        return this.getPopoutKey(this.panes.get(paneId)) || `pane:${paneId}`;
    }

    isPanePoppedOut(pane) {
        const key = this.getPopoutKey(pane);
        if (!key) return false;
        const popoutWindow = this.popoutWindows.get(key);
        if (popoutWindow && !popoutWindow.closed) return true;
        const state = this.popoutStates.get(key);
        return !!state && Date.now() - (state.lastSeen || 0) <= this.popoutStaleMs;
    }

    clearPopoutTracking(popoutKey) {
        if (!popoutKey) return;
        this.cancelPendingPopoutClose(popoutKey);
        const interval = this.popoutIntervals.get(popoutKey);
        if (interval) {
            clearInterval(interval);
            this.popoutIntervals.delete(popoutKey);
        }
        this.popoutWindows.delete(popoutKey);
        this.popoutStates.delete(popoutKey);
        this.savePopoutStates();
    }

    suppressPopoutTracking(popoutKey) {
        if (!popoutKey) return;
        this.popoutSuppressUntil.set(popoutKey, Infinity);
    }

    setPoppedOutContainers(popoutKey) {
        for (const pane of this.panes.values()) {
            if (this.getPopoutKey(pane) === popoutKey) {
                document.getElementById(`pane-${pane.id}`)?.classList.add('popped-out');
                if (pane.type === 'terminal') this.suspendTerminal(pane.id);
            }
        }
        this.updateKeybarVisibility();
    }

    clearPoppedOutContainers(popoutKey) {
        for (const pane of this.panes.values()) {
            if (this.getPopoutKey(pane) === popoutKey) {
                document.getElementById(`pane-${pane.id}`)?.classList.remove('popped-out');
                if (pane.type === 'terminal') this.resumeTerminal(pane.id);
            }
        }
        this.updateKeybarVisibility();
    }

    handlePopoutClosed(popoutKey) {
        const panesToReload = Array.from(this.panes.values()).filter(pane => this.getPopoutKey(pane) === popoutKey && this.isSharedPane(pane));
        this.clearPopoutTracking(popoutKey);
        this.clearPoppedOutContainers(popoutKey);
        panesToReload.forEach(pane => this.reloadSharedPaneIframe(pane));
        this.updatePaneLayout();
    }

    popOutPane(paneId) {
        const pane = this.panes.get(paneId);
        if (!pane) return;

        const popoutKey = this.getPopoutKey(pane);
        if (!popoutKey) return;
        this.popoutSuppressUntil.delete(popoutKey);

        const existingWindow = this.popoutWindows.get(popoutKey);
        if (existingWindow && !existingWindow.closed) {
            existingWindow.focus();
            return;
        }

        if (this.isPanePoppedOut(pane)) {
            this.popoutChannel?.postMessage({ type: 'webmux-popout-discover' });
            return;
        }

        if (this.popoutWindows.has(popoutKey)) {
            this.clearPopoutTracking(popoutKey);
        }

        const container = document.getElementById(`pane-${paneId}`);
        if (!container) return;

        const width = 800;
        const height = 600;
        const left = window.screenX + 50;
        const top = window.screenY + 50;

        const popoutUrl = this.url(`/p/${pane.id}/`);
        const popoutWindow = window.open(
            popoutUrl,
            `webmux-${popoutKey.replace(/[^a-zA-Z0-9_-]/g, '-')}`,
            `width=${width},height=${height},left=${left},top=${top},menubar=no,toolbar=no,location=no,status=no`
        );

        if (popoutWindow) {
            this.popoutWindows.set(popoutKey, popoutWindow);
            this.popoutStates.set(popoutKey, {
                key: popoutKey,
                paneId: pane.id,
                popoutId: null,
                backendId: pane.backendId,
                backendScope: pane.backendScope,
                href: popoutUrl,
                lastSeen: Date.now(),
            });
            this.savePopoutStates();
            this.setPoppedOutContainers(popoutKey);
            if (this.isSharedPane(pane)) {
                this.updatePaneLayout();
            }

            const checkClosed = setInterval(() => {
                if (popoutWindow.closed) {
                    this.handlePopoutClosed(popoutKey);
                }
            }, 500);
            this.popoutIntervals.set(popoutKey, checkClosed);
        }
    }

    popInPane(paneId) {
        const pane = this.panes.get(paneId);
        if (!pane) return;

        const container = document.getElementById(`pane-${paneId}`);
        const popoutKey = this.getPopoutKey(pane);
        this.suppressPopoutTracking(popoutKey);

        const popoutWindow = this.popoutWindows.get(popoutKey);
        if (popoutWindow && !popoutWindow.closed) {
            popoutWindow.close();
        }
        this.broadcastPaneOwnerMain(pane.id);
        const state = this.popoutStates.get(popoutKey);
        if (state) {
            this.popoutChannel?.postMessage({
                type: 'webmux-popout-close',
                paneId: state.paneId,
                popoutId: state.popoutId,
            });
        }
        this.clearPopoutTracking(popoutKey);
        this.clearPoppedOutContainers(popoutKey);

        container?.classList.remove('popped-out');

        if (this.isSharedPane(pane)) {
            this.reloadSharedPaneIframe(pane);
            this.updatePaneLayout();
            return;
        }

        if (pane.type === 'terminal') {
            this.updatePaneLayout();
            requestAnimationFrame(() => this.fitTerminal(pane.id));
        } else if (container) {
            this.rebuildDedicatedPaneIframe(pane, container);
        }
    }

    async refreshPane(paneId) {
        const pane = this.panes.get(paneId);
        if (!pane) return;

        if (this.isSharedPane(pane)) {
            this.logDiagnostic('iframe', 'refresh-shared', { paneId, backendId: pane.backendId, paneType: pane.type });
            const popoutKey = this.getPopoutKey(pane);
            const popoutWindow = this.popoutWindows.get(popoutKey);
            await this.flushOpenCodePaneStorage(pane, popoutWindow);
            if (popoutWindow && !popoutWindow.closed) {
                popoutWindow.location.href = this.url(`/p/${pane.id}/`);
            }
            this.reloadSharedPaneIframe(pane);
            this.updatePaneLayout();
            return;
        }

        const container = document.getElementById(`pane-${paneId}`);
        if (!container) return;

        container.classList.add('loading');
        this.logDiagnostic('iframe', 'refresh', { paneId, backendId: pane.backendId, paneType: pane.type });
        this.rebuildDedicatedPaneIframe(pane, container);
    }

    async flushOpenCodePaneStorage(pane, popoutWindow = null) {
        if (pane.type !== 'opencode') return;

        const backendKey = this.getPaneBackendKey(pane);
        const iframe = backendKey ? this.sharedIframes.get(backendKey) : null;
        const windows = [iframe?.contentWindow, popoutWindow].filter(Boolean);
        const flushes = [];
        for (const target of windows) {
            try {
                if (typeof target.__webmuxFlushOpenCodeStorage === 'function') {
                    flushes.push(Promise.resolve(target.__webmuxFlushOpenCodeStorage()));
                }
            } catch (error) {
                this.logDiagnostic('iframe', 'storage-flush-access-error', { paneId: pane.id, backendId: pane.backendId, paneType: pane.type });
            }
        }
        if (flushes.length === 0) return;

        await Promise.race([
            Promise.allSettled(flushes),
            new Promise(resolve => setTimeout(resolve, 3000)),
        ]);
    }

    // Pane Rendering
    // ==================

    rememberGroupSelection(groupId, paneId) {
        this.groupSelectionHistory = this.groupSelectionHistory.filter(selection => selection.groupId !== groupId);
        this.groupSelectionHistory.push({ groupId, paneId });
    }

    forgetGroupSelection(groupId) {
        this.groupSelectionHistory = this.groupSelectionHistory.filter(selection => selection.groupId !== groupId);
    }

    getPreviousGroupSelection() {
        while (this.groupSelectionHistory.length > 0) {
            const selection = this.groupSelectionHistory[this.groupSelectionHistory.length - 1];
            const group = this.groups.get(selection.groupId);
            if (group?.paneIds.length > 0) return selection;
            this.groupSelectionHistory.pop();
        }
        return null;
    }

    activateGroup(groupId, focusPaneId = null, options = {}) {
        const group = this.groups.get(groupId);
        if (!group) return;

        const visualPaneIds = this.getGroupPaneIdsInVisualOrder(group);
        const paneToFocus = focusPaneId && group.paneIds.includes(focusPaneId)
            ? focusPaneId
            : visualPaneIds[0];

        const pane = this.panes.get(paneToFocus);
        if (!document.hidden) this.clearPaneAttentionOnFocus(paneToFocus);
        if (this.activeGroupId === groupId && this.focusedPaneId === paneToFocus && this.isSharedPane(pane)) {
            return;
        }

        this.clearAllHighlights();

        this.activeGroupId = groupId;
        this.focusedPaneId = paneToFocus;
        this.rememberGroupSelection(groupId, paneToFocus);
        this.updateSidebarActiveStates();
        this.noPaneEl.classList.add('hidden');
        this.updateKeybarVisibility();
        this.updatePaneLayout();

        // Update mobile toolbar
        this.updateMobileToolbar();

        this.focusPane(paneToFocus, { save: false });
        if (options.save !== false) this.saveUIState();
    }

    updateSidebarActiveStates() {
        document.querySelectorAll('.group-container').forEach(container => {
            const groupId = container.id.replace('group-', '');
            const group = this.groups.get(groupId);
            const activePaneInGroup = this.activeGroupId === groupId && group?.paneIds.includes(this.focusedPaneId);

            container.querySelector('.group-header')?.classList.toggle('active', !!activePaneInGroup);
            container.querySelectorAll('.pane-item').forEach(item => {
                const isSinglePaneItem = !item.classList.contains('sub-item');
                const isActivePane = activePaneInGroup && item.dataset.paneId === this.focusedPaneId;
                item.classList.toggle('active', isSinglePaneItem ? this.activeGroupId === groupId && isActivePane : isActivePane);
            });
        });
    }

    focusPane(paneId, options = {}) {
        const container = document.getElementById(`pane-${paneId}`);
        if (!container) return;
        if (!document.hidden) this.clearPaneAttentionOnFocus(paneId);

        // Track focused pane for keybar targeting in split groups
        const focusChanged = this.focusedPaneId !== paneId;
        this.focusedPaneId = paneId;
        const activeGroup = this.groups.get(this.activeGroupId);
        if (activeGroup?.paneIds.includes(paneId)) {
            this.rememberGroupSelection(activeGroup.id, paneId);
        }
        this.updateSidebarActiveStates();
        this.updateKeybarVisibility();
        this.updateMobileToolbar();
        if (focusChanged && options.save !== false) this.saveUIState();

        const pane = this.panes.get(paneId);
        if (this.isSharedPane(pane)) {
            const iframe = this.sharedIframes.get(pane.backendId);
            if (!this.isPanePoppedOut(pane) && iframe?.dataset.activePaneId !== paneId) {
                this.updatePaneLayout();
            }
        }

        // Don't focus if popped out
        if (container.classList.contains('popped-out')) return;

        if (pane?.type === 'terminal') {
            if (!this.mobileMode || this.mobileTerminalMode !== 'scroll') {
                setTimeout(() => this.terminals.get(paneId)?.terminal.focus(), 0);
            }
            return;
        }

        const iframe = this.isSharedPane(pane)
            ? this.sharedIframes.get(pane.backendId)
            : container.querySelector('iframe');
        if (!iframe) return;

        // Delay to ensure layout is complete after tab switch
        setTimeout(() => {
            try {
                // Blur active element in parent document first
                document.activeElement?.blur();

                // Focus the iframe's window, then the xterm textarea
                iframe.contentWindow?.focus();
                iframe.contentDocument?.querySelector('.xterm-helper-textarea')?.focus();
            } catch (e) {
                // Cross-origin fallback for proxied HTTP panes.
                iframe.focus();
            }
        }, 100);
    }

    isSharedPane(pane) {
        return pane?.backendScope === 'shared' && pane.backendId;
    }

    getPaneBackendKey(pane) {
        return this.isSharedPane(pane) ? pane.backendId : pane?.id;
    }

    paneSupportsKeybarInput(pane) {
        if (!pane) return false;
        return this.paneTypes.find(type => type.type === pane.type)?.supportsKeybar === true;
    }

    isKeybarVisibleForCurrentPane() {
        const pane = this.panes.get(this.focusedPaneId);
        return !!pane && this.paneSupportsKeybarInput(pane) && !this.keybarUserHidden;
    }

    updateKeybarVisibility() {
        const pane = this.panes.get(this.focusedPaneId);
        const enabled = this.isKeybarVisibleForCurrentPane();
        const poppedOut = this.isPanePoppedOut(pane);
        const visible = enabled && !poppedOut;
        const paneAnchored = this.settings?.keybar?.anchor === 'pane';
        const rowDividerPosition = paneAnchored ? null : this.captureRowDividerPosition();
        const previouslyAnchoredPaneId = document.querySelector('.pane-container.pane-keybar-active')?.dataset.paneId;
        if (!this.keybar.classList.contains('hidden') && !this.keybar.classList.contains('reserved')) {
            const currentHeight = this.keybar.getBoundingClientRect().height;
            if (currentHeight > 0) {
                this.paneDisplayContainer.style.setProperty('--keybar-reserved-height', `${currentHeight}px`);
            }
        }

        document.querySelectorAll('.pane-container.pane-keybar-active').forEach(container => {
            container.classList.remove('pane-keybar-active');
            container.style.removeProperty('--pane-keybar-height');
        });

        const paneContainer = visible && paneAnchored
            ? document.getElementById(`pane-${this.focusedPaneId}`)
            : null;
        if (paneContainer) {
            paneContainer.appendChild(this.keybar);
            paneContainer.classList.add('pane-keybar-active');
        } else if (this.keybar.parentElement !== this.paneDisplayContainer) {
            this.paneDisplayContainer.appendChild(this.keybar);
        }

        const reserved = !visible
            && !paneAnchored
            && !this.keybarUserHidden
            && !poppedOut
            && this.hasMixedKeybarSupportAtBottom();
        this.keybar.classList.toggle('reserved', reserved);
        this.keybar.classList.toggle('hidden', !visible && !reserved);
        this.keybar.setAttribute('aria-hidden', String(!visible));
        this.keybarToggle.classList.toggle('active', enabled);
        if (rowDividerPosition) this.restoreRowDividerPosition(rowDividerPosition);
        this.positionDividerControl();

        if (paneContainer || previouslyAnchoredPaneId) {
            requestAnimationFrame(() => {
                if (paneContainer?.classList.contains('pane-keybar-active')) {
                    paneContainer.style.setProperty('--pane-keybar-height', `${this.keybar.offsetHeight}px`);
                    this.fitTerminal(pane.id);
                }
                if (previouslyAnchoredPaneId && (!paneContainer || previouslyAnchoredPaneId !== pane?.id)) {
                    this.fitTerminal(previouslyAnchoredPaneId);
                }
            });
        }
        this.scheduleSharedIframePosition();
        this.updateMobileKeybarVisibility();
    }

    captureRowDividerPosition() {
        const divider = this.paneDisplay.querySelector('.split-divider-v');
        if (!divider) return null;
        const containerRect = this.paneDisplay.getBoundingClientRect();
        const dividerRect = divider.getBoundingClientRect();
        return {
            height: containerRect.height,
            index: Number(divider.dataset.index),
            offset: dividerRect.top + dividerRect.height / 2 - containerRect.top
        };
    }

    restoreRowDividerPosition(snapshot) {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup?.splitRatio) return;

        const containerRect = this.paneDisplay.getBoundingClientRect();
        if (Math.abs(snapshot.height - containerRect.height) < 0.5) return;
        const divider = this.paneDisplay.querySelector(`.split-divider-v[data-index="${snapshot.index}"]`);
        if (!divider) return;

        const dividerRect = divider.getBoundingClientRect();
        const currentOffset = dividerRect.top + dividerRect.height / 2 - containerRect.top;
        const gap = parseFloat(getComputedStyle(this.paneDisplay).rowGap) || 0;
        const flexibleHeight = containerRect.height - dividerRect.height - 2 * gap;
        if (flexibleHeight <= 0) return;

        const ratio = activeGroup.splitRatio[snapshot.index] + (snapshot.offset - currentOffset) / flexibleHeight;
        activeGroup.splitRatio[snapshot.index] = Math.max(0.1, Math.min(0.9, ratio));
        this.applyGridTemplate(activeGroup.layout, activeGroup.splitRatio, activeGroup.paneIds.length);
    }

    hasMixedKeybarSupportAtBottom() {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.paneIds.length < 2) return false;

        const paneRect = this.paneDisplay.getBoundingClientRect();
        const bottomPanes = activeGroup.paneIds
            .map(paneId => ({ pane: this.panes.get(paneId), container: document.getElementById(`pane-${paneId}`) }))
            .filter(({ container }) => container?.classList.contains('visible'))
            .filter(({ container }) => Math.abs(container.getBoundingClientRect().bottom - paneRect.bottom) < 2);
        if (bottomPanes.length < 2) return false;

        const support = bottomPanes.map(({ pane }) => this.paneSupportsKeybarInput(pane));
        return support.some(Boolean) && support.some(value => !value);
    }

    updateMobileKeybarVisibility() {
        if (!this.mobileBottomToolbar) return;

        const visible = this.isKeybarVisibleForCurrentPane();
        this.mobileBottomToolbar.classList.toggle('mobile-keybar-hidden', !visible);
        this.mobileBottomToolbar.querySelector('.mobile-toolbar-center')?.classList.toggle('hidden', !visible);
    }

    paneIframeSrc(paneId, options = {}) {
        const suffix = options.forceReload ? `?wm_reload=${Date.now().toString(36)}` : '';
        return this.url(`/p/${paneId}/${suffix}`);
    }

    createPaneIframe(pane, options = {}) {
        const iframe = document.createElement('iframe');
        iframe.className = 'pane-iframe';
        iframe.title = `${this.getPaneTypeLabel(pane)} pane: ${this.getPaneDisplayName(pane)}`;
        iframe.allow = 'clipboard-read; clipboard-write';
        iframe.dataset.paneId = pane.id;
        iframe.dataset.srcPaneId = pane.id;
        iframe.dataset.backendId = this.getPaneBackendKey(pane) || pane.id;
        iframe.dataset.createdAt = String(Date.now());
        this.logDiagnostic('iframe', 'create', { paneId: pane.id, backendId: pane.backendId, paneType: pane.type, path: `/p/${pane.id}/` });

        // Listen for iframe navigation, which can happen when an HTTP backend dies.
        // Only trigger on subsequent loads (not the initial load)
        let initialLoad = true;
        iframe.addEventListener('load', () => {
            iframe.dataset.loaded = 'true';
            this.logDiagnostic('iframe', 'load', {
                paneId: iframe.dataset.paneId,
                backendId: iframe.dataset.backendId,
                paneType: this.panes.get(iframe.dataset.paneId)?.type || '',
                ageMs: Date.now() - Number(iframe.dataset.createdAt || Date.now())
            });
            const hostContainer = iframe.closest('.pane-container') || document.getElementById(`pane-${iframe.dataset.activePaneId}`);
            hostContainer?.classList.remove('loading');

            if ((this.panes.get(iframe.dataset.paneId)?.type || '') === 'opencode') {
                this.loadServerInfo().catch(() => {});
            }

            if (initialLoad) {
                initialLoad = false;

                // Track focus changes inside the iframe for keybar targeting.
                // Clicks inside an iframe don't propagate to the parent document,
                // so without this the keybar sends keys to the previously-focused pane.
                try {
                    iframe.contentWindow.addEventListener('focus', () => {
                        const paneId = iframe.dataset.paneId;
                        const focusChanged = this.focusedPaneId !== paneId;
                        this.focusedPaneId = paneId;
                        if (!document.hidden) this.clearPaneAttentionOnFocus(paneId);
                        this.updateKeybarVisibility();
                        this.updateMobileToolbar();
                        if (focusChanged && document.hasFocus()) this.saveUIState();
                    });
                } catch (e) {
                    // Cross-origin fallback - shouldn't happen since we proxy panes
                }
                return;
            }
            // iframe reloaded - pane likely died, check immediately
            this.checkPaneHealth({ savePassiveChanges: false });
        });

        // Also listen for errors to show loading failed
        iframe.addEventListener('error', () => {
            this.logDiagnostic('iframe', 'error', { paneId: iframe.dataset.paneId, backendId: iframe.dataset.backendId });
            if (!iframe.dataset.loaded) {
                const hostContainer = iframe.closest('.pane-container') || document.getElementById(`pane-${iframe.dataset.activePaneId}`);
                hostContainer?.querySelector('.pane-loading p')?.replaceChildren('Failed to connect');
            }
        });

        // Assign src after handlers are attached. Fast local panes can load
        // before a later load listener is registered, leaving the iframe hidden.
        this.logDiagnostic('iframe', 'src', { paneId: pane.id, backendId: pane.backendId, paneType: pane.type, path: `/p/${pane.id}/` });
        iframe.src = this.paneIframeSrc(pane.id, options);

        return iframe;
    }

    terminalTheme() {
        const colors = this.settings?.terminal || this.getDefaultSettings().terminal;
        return {
            background: colors.base00,
            foreground: colors.base05,
            cursor: colors.base06,
            cursorAccent: colors.base00,
            selectionBackground: colors.base02,
            scrollbarSliderBackground: '#00000000',
            scrollbarSliderHoverBackground: '#00000000',
            scrollbarSliderActiveBackground: '#00000000',
            overviewRulerBorder: '#00000000',
            black: colors.base00,
            red: colors.base08,
            green: colors.base0B,
            yellow: colors.base0A,
            blue: colors.base0D,
            magenta: colors.base0E,
            cyan: colors.base0C,
            white: colors.base05,
            brightBlack: colors.base03,
            brightRed: colors.base12,
            brightGreen: colors.base14,
            brightYellow: colors.base13,
            brightBlue: colors.base16,
            brightMagenta: colors.base17,
            brightCyan: colors.base15,
            brightWhite: colors.base07,
        };
    }

    copyTerminalSelection(terminal) {
        const selection = terminal.getSelection();
        if (!selection) return false;
        const fallbackCopy = () => {
            const textarea = document.createElement('textarea');
            textarea.value = selection;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.select();
            document.execCommand('copy');
            textarea.remove();
            terminal.focus();
        };
        if (navigator.clipboard?.writeText) navigator.clipboard.writeText(selection).catch(fallbackCopy);
        else fallbackCopy();
        return true;
    }

    sendTerminalInput(socket, data) {
        if (socket?.readyState !== WebSocket.OPEN) return;
        const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
        for (let offset = 0; offset < bytes.length; offset += 32 * 1024) {
            socket.send(bytes.subarray(offset, offset + 32 * 1024));
        }
    }

    async handleTerminalFilePaste(event, terminal) {
        const files = Array.from(event.clipboardData?.files || []);
        if (files.length === 0) return;
        event.preventDefault();
        event.stopPropagation();

        const formData = new FormData();
        for (const file of files) formData.append('files', file, file.name || 'clipboard-data');
        try {
            const response = await fetch(this.url('/api/clipboard/files'), { method: 'POST', body: formData });
            if (!response.ok) throw new Error(`upload failed (${response.status})`);
            const result = await response.json();
            const paths = Array.isArray(result.uploaded) ? result.uploaded : [];
            if (paths.length === 0) throw new Error('upload returned no paths');
            const quotedPaths = paths.map(path => /^[A-Za-z0-9_./-]+$/.test(path)
                ? path
                : `'${path.replaceAll("'", "'\\''")}'`);
            terminal.paste(quotedPaths.join(' '));
        } catch (error) {
            console.error('[clipboard] File paste failed:', error);
            this.toastError('Clipboard file paste failed');
        } finally {
            terminal.focus();
        }
    }

    ensureTerminal(pane, container) {
        if (this.isPanePoppedOut(pane)) return;
        if (this.terminals.has(pane.id)) {
            requestAnimationFrame(() => this.fitTerminal(pane.id));
            return;
        }

        const rect = container.getBoundingClientRect();
        if (!container.classList.contains('visible') || rect.width <= 0 || rect.height <= 0) {
            if (this.pendingDedicatedIframeMounts.has(pane.id)) return;
            const frame = requestAnimationFrame(() => {
                this.pendingDedicatedIframeMounts.delete(pane.id);
                this.ensureTerminal(pane, container);
            });
            this.pendingDedicatedIframeMounts.set(pane.id, frame);
            return;
        }

        const theme = this.terminalTheme();
        const host = document.createElement('div');
        host.className = 'terminal-host';
        host.dataset.paneId = pane.id;
        host.style.setProperty('--terminal-background', theme.background);
        const before = container.querySelector('.shared-mirror-placeholder') || container.querySelector('.popout-placeholder');
        if (before) container.insertBefore(host, before);
        else container.appendChild(host);

        const terminal = new Terminal({
            fontSize: 14,
            fontFamily: 'JetBrains Mono, Fira Code, SF Mono, Menlo, Monaco, Courier New, monospace',
            theme,
            scrollback: 50000,
            rightClickSelectsWord: false,
            scrollOnUserInput: true,
            allowProposedApi: true,
            disableStdin: true,
        });
        const fitAddon = new FitAddon.FitAddon();
        terminal.loadAddon(fitAddon);
        terminal.open(host);
        host.addEventListener('paste', event => this.handleTerminalFilePaste(event, terminal), true);
        let shiftSelecting = false;
        host.addEventListener('mousedown', event => {
            shiftSelecting = event.button === 0 && event.shiftKey;
        }, true);
        host.addEventListener('mouseup', event => {
            if (shiftSelecting && event.button === 0 && this.copyTerminalSelection(terminal)) {
                terminal.clearSelection();
            }
            shiftSelecting = false;
        }, true);
        terminal.attachCustomKeyEventHandler(event => {
            if (event.type !== 'keydown' || !event.ctrlKey || !event.shiftKey || event.key.toLowerCase() !== 'c') {
                return true;
            }
            return !this.copyTerminalSelection(terminal);
        });
        registerTerminalLinks(terminal);

        let webglAddon = null;
        try {
            webglAddon = new WebglAddon.WebglAddon();
            webglAddon.onContextLoss(() => {
                webglAddon.dispose();
                webglAddon = null;
                this.logDiagnostic('terminal', 'webgl-context-lost', { paneId: pane.id });
            });
            terminal.loadAddon(webglAddon);
        } catch (error) {
            webglAddon?.dispose();
            webglAddon = null;
            this.logDiagnostic('terminal', 'webgl-unavailable', { paneId: pane.id, data: { error: error.message } });
        }

        const imageAddon = new ImageAddon.ImageAddon({
            enableSizeReports: true,
            pixelLimit: 4 * 1024 * 1024,
            sixelSupport: true,
            sixelScrolling: true,
            sixelPaletteLimit: 256,
            sixelSizeLimit: 8 * 1024 * 1024,
            storageLimit: 32,
            showPlaceholder: true,
            iipSupport: false,
        });
        terminal.loadAddon(imageAddon);

        const entry = {
            terminal,
            fitAddon,
            webglAddon,
            imageAddon,
            socket: null,
            reconnectTimer: null,
            disposed: false,
            suspended: false,
            resizeObserver: null,
            resizeFallbackTimer: null,
            scrollDisposable: null,
            mobileScrollCleanup: null,
        };
        this.terminals.set(pane.id, entry);

        entry.scrollDisposable = terminal.onScroll(() => this.updateMobileTerminalControls());
        entry.mobileScrollCleanup = this.setupMobileTerminalScrolling(pane.id, entry, host);
        this.applyMobileTerminalMode();

        terminal.onData(data => {
            this.sendTerminalInput(entry.socket, new TextEncoder().encode(data));
        });
        terminal.onBinary(data => {
            this.sendTerminalInput(entry.socket, Uint8Array.from(data, char => char.charCodeAt(0)));
        });
        terminal.onResize(({ cols, rows }) => this.sendTerminalResize(entry, cols, rows));
        terminal.onBell(() => this.markPaneAttention(pane.id, false, 'terminal.bell'));

        if ('ResizeObserver' in window) {
            entry.resizeObserver = new ResizeObserver(() => this.fitTerminal(pane.id));
            entry.resizeObserver.observe(container);
        } else {
            let lastWidth = 0;
            let lastHeight = 0;
            entry.resizeFallbackTimer = setInterval(() => {
                const rect = container.getBoundingClientRect();
                const width = Math.round(rect.width);
                const height = Math.round(rect.height);
                if (width === lastWidth && height === lastHeight) return;
                lastWidth = width;
                lastHeight = height;
                this.fitTerminal(pane.id);
            }, 250);
        }
        this.connectTerminal(pane.id, entry);
        requestAnimationFrame(() => this.fitTerminal(pane.id));
    }

    setupMobileTerminalScrolling(paneId, entry, host) {
        let activePointerId = null;
        let lastY = 0;
        let lastTime = 0;
        let velocity = 0;
        let lineRemainder = 0;
        let animationFrame = null;

        const scrolling = () => this.mobileMode && this.mobileTerminalMode === 'scroll';
        const lineHeight = () => {
            const screenHeight = entry.terminal.element?.querySelector('.xterm-screen')?.getBoundingClientRect().height || 0;
            return screenHeight > 0 ? screenHeight / entry.terminal.rows : entry.terminal.options.fontSize * 1.2;
        };
        const scrollPixels = pixels => {
            lineRemainder += pixels / Math.max(1, lineHeight());
            const lines = Math.trunc(lineRemainder);
            if (!lines) return;
            lineRemainder -= lines;
            entry.terminal.scrollLines(lines);
        };
        const stopMomentum = () => {
            if (animationFrame !== null) cancelAnimationFrame(animationFrame);
            animationFrame = null;
        };
        const startMomentum = () => {
            stopMomentum();
            let previous = performance.now();
            const step = now => {
                const elapsed = Math.min(32, now - previous);
                previous = now;
                scrollPixels(velocity * elapsed);
                velocity *= Math.pow(0.92, elapsed / 16);
                if (Math.abs(velocity) < 0.02 || !scrolling()) {
                    animationFrame = null;
                    return;
                }
                animationFrame = requestAnimationFrame(step);
            };
            if (Math.abs(velocity) >= 0.02) animationFrame = requestAnimationFrame(step);
        };
        const blockTouch = event => {
            if (!scrolling()) return;
            event.preventDefault();
            event.stopImmediatePropagation();
        };
        const pointerDown = event => {
            if (!scrolling() || (event.pointerType === 'mouse' && event.button !== 0)) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            stopMomentum();
            activePointerId = event.pointerId;
            lastY = event.clientY;
            lastTime = performance.now();
            velocity = 0;
            lineRemainder = 0;
            this.mobileScrollPaneId = paneId;
            host.setPointerCapture?.(event.pointerId);
            this.updateMobileTerminalControls();
        };
        const pointerMove = event => {
            if (event.pointerId !== activePointerId || !scrolling()) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            const now = performance.now();
            const elapsed = Math.max(1, now - lastTime);
            const pixels = lastY - event.clientY;
            velocity = pixels / elapsed;
            scrollPixels(pixels);
            lastY = event.clientY;
            lastTime = now;
        };
        const pointerEnd = event => {
            if (event.pointerId !== activePointerId) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            if (host.hasPointerCapture?.(event.pointerId)) host.releasePointerCapture(event.pointerId);
            activePointerId = null;
            startMomentum();
        };
        const suppressClick = event => {
            if (!scrolling()) return;
            event.preventDefault();
            event.stopImmediatePropagation();
        };

        host.addEventListener('pointerdown', pointerDown, true);
        host.addEventListener('pointermove', pointerMove, true);
        host.addEventListener('pointerup', pointerEnd, true);
        host.addEventListener('pointercancel', pointerEnd, true);
        host.addEventListener('touchstart', blockTouch, { capture: true, passive: false });
        host.addEventListener('touchmove', blockTouch, { capture: true, passive: false });
        host.addEventListener('click', suppressClick, true);

        return () => {
            stopMomentum();
            host.removeEventListener('pointerdown', pointerDown, true);
            host.removeEventListener('pointermove', pointerMove, true);
            host.removeEventListener('pointerup', pointerEnd, true);
            host.removeEventListener('pointercancel', pointerEnd, true);
            host.removeEventListener('touchstart', blockTouch, true);
            host.removeEventListener('touchmove', blockTouch, true);
            host.removeEventListener('click', suppressClick, true);
        };
    }

    connectTerminal(paneId, entry) {
        if (this.connectionMode !== 'active' || entry.disposed || entry.suspended || !this.panes.has(paneId)) return;
        const socket = new WebSocket(this.wsUrl(`/api/panes/${encodeURIComponent(paneId)}/terminal`));
        socket.binaryType = 'arraybuffer';
        entry.socket = socket;

        socket.onopen = () => {
            if (entry.disposed || entry.socket !== socket) return;
            entry.terminal.options.disableStdin = false;
            document.getElementById(`pane-${paneId}`)?.classList.remove('loading');
            this.fitTerminal(paneId);
            this.sendTerminalResize(entry, entry.terminal.cols, entry.terminal.rows);
            if (this.focusedPaneId === paneId && (!this.mobileMode || this.mobileTerminalMode !== 'scroll')) {
                entry.terminal.focus();
            }
            this.logDiagnostic('terminal', 'open', { paneId });
        };
        socket.onmessage = event => {
            if (entry.disposed || entry.socket !== socket) return;
            if (event.data instanceof ArrayBuffer) {
                entry.terminal.write(new Uint8Array(event.data));
            } else if (event.data instanceof Blob) {
                event.data.arrayBuffer().then(data => entry.terminal.write(new Uint8Array(data)));
            }
        };
        socket.onerror = () => socket.close();
        socket.onclose = () => {
            if (entry.disposed || entry.socket !== socket) return;
            this.logDiagnostic('terminal', 'close', { paneId });
            entry.socket = null;
            entry.terminal.options.disableStdin = true;
            if (entry.suspended || this.connectionMode !== 'active') return;
            document.getElementById(`pane-${paneId}`)?.classList.add('loading');
            entry.reconnectTimer = setTimeout(() => {
                entry.reconnectTimer = null;
                this.connectTerminal(paneId, entry);
            }, 1000);
        };
    }

    suspendTerminal(paneId) {
        const entry = this.terminals.get(paneId);
        if (!entry || entry.disposed || entry.suspended) return;
        entry.suspended = true;
        entry.terminal.options.disableStdin = true;
        if (entry.reconnectTimer) {
            clearTimeout(entry.reconnectTimer);
            entry.reconnectTimer = null;
        }
        const socket = entry.socket;
        entry.socket = null;
        socket?.close(1000, 'terminal popped out');
    }

    resumeTerminal(paneId) {
        const entry = this.terminals.get(paneId);
        if (!entry || entry.disposed || !entry.suspended) return;
        entry.suspended = false;
        this.connectTerminal(paneId, entry);
        requestAnimationFrame(() => this.fitTerminal(paneId));
    }

    sendTerminalResize(entry, cols, rows) {
        if (entry.socket?.readyState !== WebSocket.OPEN) return;
        const rect = entry.terminal.element?.querySelector('.xterm-screen')?.getBoundingClientRect();
        entry.socket.send(JSON.stringify({
            type: 'resize',
            cols,
            rows,
            pixelWidth: Math.round(rect?.width || 0),
            pixelHeight: Math.round(rect?.height || 0),
        }));
    }

    fitTerminal(paneId) {
        const entry = this.terminals.get(paneId);
        const container = document.getElementById(`pane-${paneId}`);
        if (!entry || !container?.classList.contains('visible') || container.classList.contains('popped-out')) return;
        const rect = container.getBoundingClientRect();
        if (rect.width <= 0 || rect.height <= 0) return;
        try {
            entry.fitAddon.fit();
        } catch (error) {
            this.logDiagnostic('terminal', 'fit-error', { paneId, data: { error: error.message } });
        }
    }

    disposeTerminal(paneId) {
        const entry = this.terminals.get(paneId);
        if (!entry) return;
        this.terminals.delete(paneId);
        entry.disposed = true;
        if (entry.reconnectTimer) clearTimeout(entry.reconnectTimer);
        entry.resizeObserver?.disconnect();
        if (entry.resizeFallbackTimer) clearInterval(entry.resizeFallbackTimer);
        entry.scrollDisposable?.dispose();
        entry.mobileScrollCleanup?.();
        entry.socket?.close(1000, 'terminal disposed');
        entry.terminal.dispose();
    }

    ensureDedicatedPaneIframe(pane, container) {
        if (!pane || this.isSharedPane(pane) || !container || this.isPanePoppedOut(pane)) return;
        if (pane.type === 'terminal') {
            this.ensureTerminal(pane, container);
            return;
        }
        if (container.querySelector('iframe')) return;

        const rect = container.getBoundingClientRect();
        if (!container.classList.contains('visible') || rect.width <= 0 || rect.height <= 0) {
            if (this.pendingDedicatedIframeMounts.has(pane.id)) return;
            const frame = requestAnimationFrame(() => {
                this.pendingDedicatedIframeMounts.delete(pane.id);
                this.ensureDedicatedPaneIframe(pane, container);
            });
            this.pendingDedicatedIframeMounts.set(pane.id, frame);
            return;
        }

        container.classList.add('loading');
        const iframe = this.createPaneIframe(pane);
        const before = container.querySelector('.shared-mirror-placeholder') || container.querySelector('.popout-placeholder');
        if (before) container.insertBefore(iframe, before);
        else container.appendChild(iframe);

        requestAnimationFrame(() => {
            try {
                iframe.contentWindow?.dispatchEvent(new Event('resize'));
            } catch (e) {}
        });
    }

    rebuildDedicatedPaneIframe(pane, container) {
        if (!pane || this.isSharedPane(pane) || !container) return;
        if (pane.type === 'terminal') {
            this.disposeTerminal(pane.id);
            container.querySelector('.terminal-host')?.remove();
            container.classList.add('loading');
            this.ensureTerminal(pane, container);
            return;
        }
        const pending = this.pendingDedicatedIframeMounts.get(pane.id);
        if (pending) {
            cancelAnimationFrame(pending);
            this.pendingDedicatedIframeMounts.delete(pane.id);
        }
        container.querySelector('iframe')?.remove();
        container.classList.add('loading');
        const iframe = this.createPaneIframe(pane, { forceReload: true });
        const before = container.querySelector('.shared-mirror-placeholder') || container.querySelector('.popout-placeholder');
        if (before) container.insertBefore(iframe, before);
        else container.appendChild(iframe);
    }

    getSharedPaneIframe(pane) {
        const backendKey = this.getPaneBackendKey(pane);
        let iframe = this.sharedIframes.get(backendKey);
        if (!iframe) {
            iframe = this.createPaneIframe(pane);
            this.sharedIframes.set(backendKey, iframe);
            this.sharedIframeLayer?.appendChild(iframe);
        }
        return iframe;
    }

    reloadSharedPaneIframe(pane) {
        const backendKey = this.getPaneBackendKey(pane);
        if (!backendKey) return;

        const iframe = this.sharedIframes.get(backendKey);
        if (iframe) {
            this.logDiagnostic('iframe', 'reload-shared', { paneId: pane.id, backendId: backendKey, paneType: pane.type });
            iframe.remove();
            this.sharedIframes.delete(backendKey);
        }

        for (const candidate of this.panes.values()) {
            if (candidate.backendId !== pane.backendId) continue;
            document.getElementById(`pane-${candidate.id}`)?.classList.add('loading');
        }
    }

    hideSharedIframes() {
        for (const iframe of this.sharedIframes.values()) {
            iframe.classList.add('hidden');
            iframe.dataset.activePaneId = '';
        }
    }

    mountSharedPaneIframe(pane, container) {
        const iframe = this.getSharedPaneIframe(pane);

        if (iframe.dataset.srcPaneId && !this.panes.has(iframe.dataset.srcPaneId)) {
            iframe.dataset.loaded = '';
            iframe.dataset.srcPaneId = pane.id;
            iframe.src = this.url(`/p/${pane.id}/`);
        }

        iframe.dataset.paneId = pane.id;
        iframe.dataset.activePaneId = pane.id;
        iframe.title = `${this.getPaneTypeLabel(pane)} pane: ${this.getPaneDisplayName(pane)}`;

        this.positionSharedIframe(iframe, container);
        iframe.classList.remove('hidden');

        container.classList.toggle('loading', !iframe.dataset.loaded);
    }

    positionSharedIframes() {
        this.sharedIframePositionFrame = null;
        for (const iframe of this.sharedIframes.values()) {
            const paneId = iframe.dataset.activePaneId;
            if (!paneId) continue;
            const container = document.getElementById(`pane-${paneId}`);
            if (container?.classList.contains('visible')) {
                this.positionSharedIframe(iframe, container);
            }
        }
    }

    scheduleSharedIframePosition() {
        if (this.sharedIframePositionFrame) return;
        this.sharedIframePositionFrame = requestAnimationFrame(() => this.positionSharedIframes());
    }

    positionSharedIframe(iframe, container) {
        const paneDisplayRect = this.paneDisplay.getBoundingClientRect();
        const containerRect = container.getBoundingClientRect();

        const nextLeft = `${containerRect.left - paneDisplayRect.left}px`;
        const nextTop = `${containerRect.top - paneDisplayRect.top}px`;
        const nextWidth = `${containerRect.width}px`;
        const nextHeight = `${containerRect.height}px`;

        const changed = iframe.style.left !== nextLeft
            || iframe.style.top !== nextTop
            || iframe.style.width !== nextWidth
            || iframe.style.height !== nextHeight;

        iframe.style.left = nextLeft;
        iframe.style.top = nextTop;
        iframe.style.width = nextWidth;
        iframe.style.height = nextHeight;

        if (!changed) return;

        requestAnimationFrame(() => {
            try {
                iframe.contentWindow?.dispatchEvent(new Event('resize'));
            } catch (e) {
                // Cross-origin fallback - browser layout still resizes the iframe element.
            }
        });
    }

    getLiveSharedPaneIds(paneIds) {
        const liveByBackend = new Map();
        for (const paneId of paneIds) {
            const pane = this.panes.get(paneId);
            if (this.isSharedPane(pane) && !liveByBackend.has(pane.backendId)) {
                liveByBackend.set(pane.backendId, paneId);
            }
        }

        const focusedPane = this.panes.get(this.focusedPaneId);
        if (this.isSharedPane(focusedPane) && paneIds.includes(this.focusedPaneId)) {
            liveByBackend.set(focusedPane.backendId, this.focusedPaneId);
        }

        return liveByBackend;
    }

    createPaneContainer(pane) {
        const container = document.createElement('div');
        container.id = `pane-${pane.id}`;
        container.className = 'pane-container loading';
        container.dataset.paneId = pane.id;

        // Loading overlay shown while the pane connects
        const loadingOverlay = document.createElement('div');
        loadingOverlay.className = 'pane-loading';
        loadingOverlay.setAttribute('role', 'status');
        loadingOverlay.setAttribute('aria-live', 'polite');
        loadingOverlay.innerHTML = `
            <div class="pane-loading-spinner" aria-hidden="true"></div>
            <p>Connecting...</p>
        `;

        const mirrorPlaceholder = document.createElement('div');
        mirrorPlaceholder.className = 'shared-mirror-placeholder hidden';
        mirrorPlaceholder.setAttribute('role', 'status');
        mirrorPlaceholder.innerHTML = `
            <p>${this.getPaneTypeLabel(pane)} is already active in this group</p>
            <small>Focus this mirror to move the live view here.</small>
        `;

        const placeholder = document.createElement('div');
        placeholder.className = 'popout-placeholder hidden';
        placeholder.setAttribute('role', 'status');
        placeholder.innerHTML = `
            <svg viewBox="0 0 24 24" width="48" height="48" aria-hidden="true">
                <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
            </svg>
            <p>${this.getPaneTypeLabel(pane)} popped out</p>
            <button class="btn btn-secondary pop-back-in" data-pane-id="${pane.id}" aria-label="Pop ${this.getPaneTypeLabel(pane)} pane back into browser window">Pop back in</button>
        `;

        placeholder.querySelector('.pop-back-in').addEventListener('click', () => {
            this.popInPane(pane.id);
        });

        // Focus this pane when clicking on the container (gaps/borders)
        container.addEventListener('click', () => {
            this.focusPane(pane.id);
        });

        container.appendChild(loadingOverlay);
        container.appendChild(mirrorPlaceholder);
        container.appendChild(placeholder);
        this.paneDisplay.appendChild(container);

        if (this.isPanePoppedOut(pane)) {
            container.classList.add('popped-out');
        }

        return container;
    }

    getPaneContainer(paneId) {
        let container = document.getElementById(`pane-${paneId}`);
        if (!container) {
            const pane = this.panes.get(paneId);
            if (pane) {
                container = this.createPaneContainer(pane);
            }
        }
        return container;
    }

    removePaneContainer(paneId) {
        this.disposeTerminal(paneId);
        const pending = this.pendingDedicatedIframeMounts.get(paneId);
        if (pending) {
            cancelAnimationFrame(pending);
            this.pendingDedicatedIframeMounts.delete(paneId);
        }
        const container = document.getElementById(`pane-${paneId}`);
        if (container) {
            if (container.contains(this.keybar)) {
                this.paneDisplayContainer.appendChild(this.keybar);
            }
            const iframe = container.querySelector('iframe');
            const backendId = iframe?.dataset.backendId;
            if (backendId && this.sharedIframes.get(backendId) === iframe) {
                iframe.remove();
                this.sharedIframes.delete(backendId);
            }
            container.remove();
        }

        for (const [backendId, sharedIframe] of this.sharedIframes) {
            if (sharedIframe.dataset.srcPaneId !== paneId) continue;
            const hasRemainingPane = Array.from(this.panes.values()).some(pane => pane.backendId === backendId);
            if (!hasRemainingPane) {
                sharedIframe.remove();
                this.sharedIframes.delete(backendId);
            }
        }
    }

    updatePaneLayout(keepControlVisible = false) {
        if (this.connectionMode !== 'active') {
            this.hideSharedIframes();
            return;
        }
        const activeGroup = this.groups.get(this.activeGroupId);

        document.querySelectorAll('.pane-container').forEach(el => {
            el.classList.remove('visible', 'pane-0', 'pane-1', 'pane-2', 'pane-3', 'expanded', 'expanded-top', 'expanded-left');
            el.style.gridArea = '';
            el.querySelector('.shared-mirror-placeholder')?.classList.add('hidden');
        });

        this.hideSharedIframes();

        document.querySelectorAll('.split-divider').forEach(el => el.remove());
        document.getElementById('divider-control')?.remove();
        this.ensureResizeOverlay();

        if (!activeGroup || activeGroup.paneIds.length === 0) {
            this.paneDisplay.className = 'layout-single';
            this.paneDisplay.style.gridTemplateColumns = '';
            this.paneDisplay.style.gridTemplateRows = '';
            this.noPaneEl?.classList.remove('hidden');
            return;
        }

        this.noPaneEl?.classList.add('hidden');

        const paneCount = activeGroup.paneIds.length;
        const layout = activeGroup.layout;
        const ratio = activeGroup.splitRatio || this.getDefaultSplitRatio(paneCount);

        // For 3-pane, add modifier class for expansion direction
        let containerClass = `layout-${layout}`;
        const expandDir = activeGroup.expandedQuadrant; // 'bottom', 'top', 'left', 'right' or legacy number

        if (paneCount === 3 && layout === 'grid') {
            // Convert legacy numeric values
            let dir = expandDir;
            if (dir === 2 || dir === undefined || dir === null) dir = 'bottom';
            if (dir === 1) dir = 'right';

            if (dir === 'top') containerClass += ' top-wide';
            else if (dir === 'left') containerClass += ' left-wide';
        }

        this.paneDisplay.className = containerClass;
        this.applyGridTemplate(layout, ratio, paneCount);

        // Force reflow to ensure grid template is applied before adding dividers
        this.paneDisplay.offsetHeight;

        // cellMapping maps pane positions to pane indices
        // If not set, use identity mapping (pane 0 -> pane 0, etc.)
        const cellMapping = activeGroup.cellMapping || activeGroup.paneIds.map((_, i) => i);
        const liveSharedPaneIds = this.getLiveSharedPaneIds(activeGroup.paneIds);

        activeGroup.paneIds.forEach((paneId, paneIndexInGroup) => {
            const container = this.getPaneContainer(paneId);
            if (container) {
                const pane = this.panes.get(paneId);

                // Find which pane this pane should occupy
                const paneIndex = cellMapping.indexOf(paneIndexInGroup);
                container.classList.add('visible', `pane-${paneIndex}`);

                if (paneCount === 3) {
                    // Convert legacy numeric values
                    let dir = expandDir;
                    if (dir === 2 || dir === undefined || dir === null) dir = 'bottom';
                    if (dir === 1) dir = 'right';

                    // Pane 0 is expanded for top-wide and left-wide layouts
                    // Pane 1 is expanded for right layout
                    // Pane 2 is expanded for bottom layout
                    if (dir === 'bottom' && paneIndex === 2) {
                        container.classList.add('expanded');
                    } else if (dir === 'right' && paneIndex === 1) {
                        container.classList.add('expanded');
                    } else if (dir === 'top' && paneIndex === 0) {
                        container.classList.add('expanded-top');
                    } else if (dir === 'left' && paneIndex === 0) {
                        container.classList.add('expanded-left');
                    }
                }

                if (this.isSharedPane(pane)) {
                    const livePaneId = liveSharedPaneIds.get(pane.backendId);
                    const placeholder = container.querySelector('.shared-mirror-placeholder');

                    if (this.isPanePoppedOut(pane)) {
                        container.classList.add('popped-out');
                        container.classList.remove('loading');
                        placeholder?.classList.add('hidden');
                    } else if (paneId === livePaneId) {
                        container.classList.remove('popped-out');
                        placeholder?.classList.add('hidden');
                        this.mountSharedPaneIframe(pane, container);
                    } else {
                        container.classList.remove('popped-out');
                        container.classList.remove('loading');
                        placeholder?.classList.remove('hidden');
                    }
                } else {
                    this.ensureDedicatedPaneIframe(pane, container);
                }
            }
        });

        this.createDividers(layout, paneCount, expandDir, keepControlVisible);
        this.updateKeybarVisibility();
    }

    ensureResizeOverlay() {
        if (!document.getElementById('resize-overlay')) {
            const overlay = document.createElement('div');
            overlay.id = 'resize-overlay';
            this.paneDisplay.appendChild(overlay);
        }
    }

    applyGridTemplate(layout, ratio, paneCount) {
        const gap = 'var(--split-gap)';

        switch (layout) {
            case 'single':
                this.paneDisplay.style.gridTemplateColumns = '1fr';
                this.paneDisplay.style.gridTemplateRows = '1fr';
                break;
            case 'horizontal':
                const hRatio = ratio[0];
                this.paneDisplay.style.gridTemplateColumns = `${hRatio}fr ${gap} ${1 - hRatio}fr`;
                this.paneDisplay.style.gridTemplateRows = '1fr';
                break;
            case 'vertical':
                const vRatio = ratio[0];
                this.paneDisplay.style.gridTemplateColumns = '1fr';
                this.paneDisplay.style.gridTemplateRows = `${vRatio}fr ${gap} ${1 - vRatio}fr`;
                break;
            case 'grid':
                const colRatio = ratio[0];
                const rowRatio = ratio[1];
                this.paneDisplay.style.gridTemplateColumns = `${colRatio}fr ${gap} ${1 - colRatio}fr`;
                this.paneDisplay.style.gridTemplateRows = `${rowRatio}fr ${gap} ${1 - rowRatio}fr`;
                break;
        }
        this.scheduleSharedIframePosition();
    }

    createDividers(layout, paneCount, expandedQuadrant = 2, keepControlVisible = false) {
        if (layout === 'single') return;

        if (layout === 'horizontal') {
            const divider = document.createElement('div');
            divider.className = 'split-divider split-divider-h';
            divider.style.gridColumn = '2';
            divider.style.gridRow = '1';
            divider.dataset.axis = 'horizontal';
            divider.dataset.index = '0';
            this.paneDisplay.appendChild(divider);
            this.bindDividerEvents(divider);
            this.createDividerControl('2-pane', null, keepControlVisible);
        } else if (layout === 'vertical') {
            const divider = document.createElement('div');
            divider.className = 'split-divider split-divider-v';
            divider.style.gridColumn = '1';
            divider.style.gridRow = '2';
            divider.dataset.axis = 'vertical';
            divider.dataset.index = '0';
            this.paneDisplay.appendChild(divider);
            this.bindDividerEvents(divider);
            this.createDividerControl('2-pane', null, keepControlVisible);
        } else if (layout === 'grid') {
            const is3Pane = paneCount === 3;

            const hDivider = document.createElement('div');
            hDivider.className = 'split-divider split-divider-h';
            hDivider.style.gridColumn = '2';
            // Convert legacy numeric values for 3-pane
            let dir = expandedQuadrant;
            if (dir === 2 || dir === undefined || dir === null) dir = 'bottom';
            if (dir === 1) dir = 'right';

            // In 3-pane: h-divider spans based on expansion direction
            // bottom/top: h-divider only in one row (small panes row)
            // left/right: h-divider spans all rows
            const isHorizontalWide = (dir === 'left' || dir === 'right');
            hDivider.style.gridRow = (is3Pane && !isHorizontalWide) ? (dir === 'bottom' ? '1' : '3') : '1 / -1';
            hDivider.dataset.axis = 'horizontal';
            hDivider.dataset.index = '0';
            this.paneDisplay.appendChild(hDivider);
            this.bindDividerEvents(hDivider);

            const vDivider = document.createElement('div');
            vDivider.className = 'split-divider split-divider-v';
            // In 3-pane: v-divider spans based on expansion direction
            // left/right: v-divider only in one column (small panes column)
            // bottom/top: v-divider spans all columns
            vDivider.style.gridColumn = (is3Pane && isHorizontalWide) ? (dir === 'right' ? '1' : '3') : '1 / -1';
            vDivider.style.gridRow = '2';
            vDivider.dataset.axis = 'vertical';
            vDivider.dataset.index = '1';
            this.paneDisplay.appendChild(vDivider);
            this.bindDividerEvents(vDivider);

            this.createDividerControl(is3Pane ? '3-pane' : '4-pane', expandedQuadrant, keepControlVisible);
        }
    }

    createDividerControl(mode, expandedQuadrant = 2, showImmediately = false) {
        // Remove existing control
        document.getElementById('divider-control')?.remove();

        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.paneIds.length < 2) return;

        const control = document.createElement('div');
        control.id = 'divider-control';
        control.className = 'divider-control';

        // Add layout class for CSS taper direction
        if (mode === '2-pane') {
            control.classList.add(`layout-${activeGroup.layout}`);
        } else {
            control.classList.add('at-crux');
        }

        if (mode === '2-pane') {
            // Simple rotate button for 2-pane
            control.innerHTML = `
                <button class="divider-control-btn" title="Rotate layout" aria-label="Rotate layout">
                    <span class="inner-dot"></span>
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 5V1l5 5-5 5V7c-3.31 0-6 2.69-6 6s2.69 6 6 6 6-2.69 6-6h2c0 4.42-3.58 8-8 8s-8-3.58-8-8 3.58-8 8-8z"/></svg>
                </button>
            `;
            control.querySelector('.divider-control-btn').addEventListener('click', (e) => {
                e.stopPropagation();
                this.handleDividerControlAction('rotate-cw');
            });

            this.paneDisplay.appendChild(control);
            this.positionDividerControl();
            return;
        }

        // For 3-pane, add class to indicate which tapers to hide
        // Convert legacy numeric values
        let dir = expandedQuadrant;
        if (dir === 2 || dir === undefined || dir === null) dir = 'bottom';
        if (dir === 1) dir = 'right';

        if (mode === '3-pane') {
            control.classList.add(`expand-${dir}`);
        }

        let menuHTML = '';

        if (mode === '3-pane') {
            // 3-pane: Top/Bottom stacked on top half, Left/Right side-by-side on bottom
            menuHTML = `
                <button data-action="expand-top" title="Top cell wide" aria-label="Expand top cell">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M3 3h18v8H3V3zm0 10h8v8H3v-8zm10 0h8v8h-8v-8z"/></svg>
                    <span>Top</span>
                </button>
                <button data-action="expand-bottom" title="Bottom cell wide" aria-label="Expand bottom cell">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M3 3h8v8H3V3zm0 10h18v8H3v-8zm10-10h8v8h-8V3z"/></svg>
                    <span>Bottom</span>
                </button>
                <button data-action="expand-left" title="Left cell wide" aria-label="Expand left cell">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M3 3h8v18H3V3zm10 0h8v8h-8V3zm0 10h8v8h-8v-8z"/></svg>
                    <span>Left</span>
                </button>
                <button data-action="expand-right" title="Right cell wide" aria-label="Expand right cell">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M13 3h8v18h-8V3zM3 3h8v8H3V3zm0 10h8v8H3v-8z"/></svg>
                    <span>Right</span>
                </button>
            `;
        } else if (mode === '4-pane') {
            // 4-pane: 2x2 grid with CCW/CW on top, FlipH/FlipV on bottom
            menuHTML = `
                <button data-action="rotate-ccw" title="Rotate counter-clockwise" aria-label="Rotate counter-clockwise">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 5V1L7 6l5 5V7c3.31 0 6 2.69 6 6s-2.69 6-6 6-6-2.69-6-6H4c0 4.42 3.58 8 8 8s8-3.58 8-8-3.58-8-8-8z"/></svg>
                    <span>CCW</span>
                </button>
                <button data-action="rotate-cw" title="Rotate clockwise" aria-label="Rotate clockwise">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M12 5V1l5 5-5 5V7c-3.31 0-6 2.69-6 6s2.69 6 6 6 6-2.69 6-6h2c0 4.42-3.58 8-8 8s-8-3.58-8-8 3.58-8 8-8z"/></svg>
                    <span>CW</span>
                </button>
                <button data-action="flip-h" title="Flip horizontally" aria-label="Flip horizontally">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M11 3v18h2V3h-2zM3 3h6v8H3V3zm0 10h6v8H3v-8zm12-10h6v8h-6V3zm0 10h6v8h-6v-8z"/></svg>
                    <span>Flip H</span>
                </button>
                <button data-action="flip-v" title="Flip vertically" aria-label="Flip vertically">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M3 11h18v2H3v-2zM3 3h8v6H3V3zm10 0h8v6h-8V3zM3 15h8v6H3v-6zm10 0h8v6h-8v-6z"/></svg>
                    <span>Flip V</span>
                </button>
            `;
        }

        control.innerHTML = `
            <div class="divider-control-indicator">
                <span class="inner-dot"></span>
                <span class="taper-h-left"></span>
                <span class="taper-h-right"></span>
                <span class="taper-v-top"></span>
                <span class="taper-v-bottom"></span>
            </div>
            <div class="divider-control-menu">
                <span class="taper-h-left"></span>
                <span class="taper-h-right"></span>
                <span class="taper-v-top"></span>
                <span class="taper-v-bottom"></span>
                ${menuHTML}
            </div>
        `;

        const menu = control.querySelector('.divider-control-menu');

        menu.querySelectorAll('button').forEach(menuBtn => {
            menuBtn.addEventListener('click', (e) => {
                e.stopPropagation();
                const action = menuBtn.dataset.action;
                this.handleDividerControlAction(action);
            });
        });

        this.paneDisplay.appendChild(control);
        this.positionDividerControl(showImmediately);
    }

    pinDividerControl() {
        const control = document.getElementById('divider-control');
        if (!control) return;

        control.classList.add('pinned');

        // Clear any existing unpin timeout
        if (this._unpinTimeout) clearTimeout(this._unpinTimeout);

        // Set up mouseenter to keep it pinned, mouseleave to start unpin timer
        const onMouseEnter = () => {
            if (this._unpinTimeout) clearTimeout(this._unpinTimeout);
        };

        const onMouseLeave = () => {
            this._unpinTimeout = setTimeout(() => {
                control.classList.remove('pinned');
            }, 300);
        };

        control.addEventListener('mouseenter', onMouseEnter);
        control.addEventListener('mouseleave', onMouseLeave);
    }

    positionDividerControl(showImmediately = false) {
        const control = document.getElementById('divider-control');
        if (!control) return;

        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup) return;

        const layout = activeGroup.layout;
        const rect = this.paneDisplay.getBoundingClientRect();

        // If container isn't laid out yet, retry after a frame
        if (rect.width === 0 || rect.height === 0) {
            requestAnimationFrame(() => this.positionDividerControl(showImmediately));
            return;
        }

        let x, y;
        const horizontalDivider = this.paneDisplay.querySelector('.split-divider-h');
        const verticalDivider = this.paneDisplay.querySelector('.split-divider-v');
        const horizontalRect = horizontalDivider?.getBoundingClientRect();
        const verticalRect = verticalDivider?.getBoundingClientRect();

        if (layout === 'horizontal') {
            if (!horizontalRect) return;
            x = horizontalRect.left + horizontalRect.width / 2 - rect.left;
            y = rect.height / 2;
        } else if (layout === 'vertical') {
            if (!verticalRect) return;
            x = rect.width / 2;
            y = verticalRect.top + verticalRect.height / 2 - rect.top;
        } else if (layout === 'grid') {
            if (!horizontalRect || !verticalRect) return;
            // Position at the crux of dividers
            x = horizontalRect.left + horizontalRect.width / 2 - rect.left;
            y = verticalRect.top + verticalRect.height / 2 - rect.top;
        }

        control.style.left = `${x}px`;
        control.style.top = `${y}px`;
        control.style.transform = 'translate(-50%, -50%)';

        // Show control after positioning (immediately if user just interacted)
        if (showImmediately) {
            control.classList.add('visible');
        } else {
            requestAnimationFrame(() => control.classList.add('visible'));
        }
    }

    handleDividerControlAction(action) {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup) return;

        const count = activeGroup.paneIds.length;
        // cellMapping maps pane positions to pane indices
        // Default is identity: [0,1,2,3] meaning pane 0 in pane 0, etc.
        const cm = activeGroup.cellMapping || activeGroup.paneIds.map((_, i) => i);

        switch (action) {
            // 2-pane rotation
            case 'rotate-cw':
                if (count === 2) {
                    if (activeGroup.layout === 'horizontal') {
                        activeGroup.layout = 'vertical';
                    } else {
                        activeGroup.layout = 'horizontal';
                        // Swap pane positions: [a,b] -> [b,a]
                        activeGroup.cellMapping = [cm[1], cm[0]];
                    }
                } else if (count === 4) {
                    // Rotate clockwise: pane positions [0,1,2,3] -> [2,0,3,1]
                    activeGroup.cellMapping = [cm[2], cm[0], cm[3], cm[1]];
                }
                break;

            case 'rotate-ccw':
                if (count === 4) {
                    // Rotate counter-clockwise: [0,1,2,3] -> [1,3,0,2]
                    activeGroup.cellMapping = [cm[1], cm[3], cm[0], cm[2]];
                }
                break;

            // 3-pane layout changes - remap so panes only move cardinally
            //
            // Each 3-pane layout has one wide pane and two small panes.
            // When switching layouts, we pick the small pane closest to the
            // target edge to become wide. Panes only move cardinally.
            //
            // Pane index positions by expandDir:
            //   bottom: pane0=TL, pane1=TR, pane2=wide-bottom
            //   top:    pane0=wide-top, pane1=BL, pane2=BR
            //   right:  pane0=TL, pane1=wide-right, pane2=BL
            //   left:   pane0=wide-left, pane1=TR, pane2=BR
            case 'expand-top':
            case 'expand-bottom':
            case 'expand-left':
            case 'expand-right':
                if (count === 3) {
                    const newDir = action.replace('expand-', '');
                    const oldDir = activeGroup.expandedQuadrant || 'bottom';

                    if (newDir !== oldDir) {
                        // Precomputed transition table for cardinal movement
                        // transitions[oldDir][newDir] = new cellMapping indices
                        // Each array shows: [newPane0, newPane1, newPane2] = [cm[x], cm[y], cm[z]]
                        //
                        // Layout pane positions:
                        //   bottom: pane0=TL, pane1=TR, pane2=wide-bottom
                        //   top:    pane0=wide-top, pane1=BL, pane2=BR
                        //   left:   pane0=wide-left, pane1=TR, pane2=BR
                        //   right:  pane0=TL, pane1=wide-right, pane2=BL
                        //
                        // Rules for cardinal movement:
                        // 1. The small pane on the target edge becomes wide
                        // 2. The old wide pane contracts to the opposite edge
                        // 3. The other small pane shifts cardinally (not diagonally)
                        const transitions = {
                            bottom: {
                                // bottom: TL=0, TR=1, wideB=2
                                // Smalls are at top. Wide is at bottom.
                                top:   [0, 2, 1], // TL→wideT, wideB→BL, TR→BR
                                left:  [0, 1, 2], // TL→wideL, TR→TR, wideB→BR
                                right: [0, 1, 2], // TL→TL, TR→wideR, wideB→BL
                            },
                            top: {
                                // top: wideT=0, BL=1, BR=2
                                // Smalls are at bottom. Wide is at top.
                                bottom: [0, 2, 1], // wideT→TL, BR→TR, BL→wideB
                                left:   [1, 0, 2], // BL→wideL, wideT→TR, BR→BR
                                right:  [0, 2, 1], // wideT→TL, BR→wideR, BL→BL
                            },
                            left: {
                                // left: wideL=0, TR=1, BR=2
                                // Smalls are at right. Wide is at left.
                                top:    [1, 0, 2], // TR→wideT, wideL→BL, BR→BR
                                bottom: [0, 1, 2], // wideL→TL, TR→TR, BR→wideB
                                right:  [0, 1, 2], // wideL→TL, TR→wideR, BR→BL
                            },
                            right: {
                                // right: TL=0, wideR=1, BL=2
                                // Smalls are at left. Wide is at right.
                                top:    [0, 2, 1], // TL→wideT, BL→BL, wideR→BR
                                bottom: [0, 1, 2], // TL→TL, wideR→TR, BL→wideB
                                left:   [0, 1, 2], // TL→wideL, wideR→TR, BL→BR
                            },
                        };

                        const t = transitions[oldDir][newDir];
                        activeGroup.cellMapping = [cm[t[0]], cm[t[1]], cm[t[2]]];
                    }
                    activeGroup.expandedQuadrant = newDir;
                }
                break;

            // 4-pane flips
            case 'flip-h':
                if (count === 4) {
                    // Flip horizontally: swap columns [0,1,2,3] -> [1,0,3,2]
                    activeGroup.cellMapping = [cm[1], cm[0], cm[3], cm[2]];
                }
                break;

            case 'flip-v':
                if (count === 4) {
                    // Flip vertically: swap rows [0,1,2,3] -> [2,3,0,1]
                    activeGroup.cellMapping = [cm[2], cm[3], cm[0], cm[1]];
                }
                break;
        }

        this.updatePaneLayout(true); // keepControlVisible = true
        this.pinDividerControl(); // Keep menu open after action
        this.refreshPaneTitles();
        this.saveUIState();
    }

    bindDividerEvents(divider) {
        divider.addEventListener('mousedown', (e) => {
            e.preventDefault();
            this.startResize(divider, e);
        });
    }

    startResize(divider, e) {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup) return;

        const axis = divider.dataset.axis;
        const index = parseInt(divider.dataset.index);
        const overlay = document.getElementById('resize-overlay');

        overlay.classList.add('active', axis === 'horizontal' ? 'col-resize' : 'row-resize');
        divider.classList.add('dragging');

        const containerRect = this.paneDisplay.getBoundingClientRect();
        const totalSize = axis === 'horizontal' ? containerRect.width : containerRect.height;

        // Hide control during resize
        const control = document.getElementById('divider-control');
        if (control) control.classList.remove('visible');

        const onMouseMove = (moveEvent) => {
            const currentPos = axis === 'horizontal' ? moveEvent.clientX : moveEvent.clientY;
            const containerStart = axis === 'horizontal' ? containerRect.left : containerRect.top;

            let newRatio = (currentPos - containerStart) / totalSize;
            newRatio = Math.max(0.1, Math.min(0.9, newRatio));

            activeGroup.splitRatio[index] = newRatio;
            this.applyGridTemplate(activeGroup.layout, activeGroup.splitRatio, activeGroup.paneIds.length);
        };

        const onMouseUp = () => {
            overlay.classList.remove('active', 'col-resize', 'row-resize');
            divider.classList.remove('dragging');
            document.removeEventListener('mousemove', onMouseMove);
            document.removeEventListener('mouseup', onMouseUp);
            // Reposition and show control after resize
            this.positionDividerControl();
            this.saveUIState();
        };

        document.addEventListener('mousemove', onMouseMove);
        document.addEventListener('mouseup', onMouseUp);
    }

    // Split/Drop Handling
    // ===================

    setupPaneDragTarget() {
        document.addEventListener('dragstart', () => {
            setTimeout(() => {
                if (this.draggedPaneId) {
                    this.showDragOverlay();
                }
            }, 0);
        });

        document.addEventListener('dragend', () => {
            this.hideDragOverlay();
        });
    }

    showDragOverlay() {
        const activeGroup = this.groups.get(this.activeGroupId);

        if (!activeGroup || activeGroup.paneIds.length >= 4) {
            return;
        }

        if (this.draggedPaneId && activeGroup.paneIds.includes(this.draggedPaneId)) {
            return;
        }

        let overlay = document.getElementById('drag-capture-overlay');
        if (!overlay) {
            overlay = document.createElement('div');
            overlay.id = 'drag-capture-overlay';
            this.paneDisplay.appendChild(overlay);
        }

        overlay.innerHTML = this.generateDropZones(activeGroup);

        overlay.querySelectorAll('.drop-zone').forEach(zone => {
            zone.addEventListener('dragenter', () => zone.classList.add('drag-over'));
            zone.addEventListener('dragleave', () => zone.classList.remove('drag-over'));
            zone.addEventListener('dragover', (e) => {
                e.preventDefault();
                e.dataTransfer.dropEffect = 'move';
            });
            zone.addEventListener('drop', (e) => {
                e.preventDefault();
                if (this.draggedPaneId) {
                    this.handleSplitDrop(e);
                }
            });
        });

        activeGroup.paneIds.forEach(paneId => {
            const container = this.getPaneContainer(paneId);
            if (container) {
                container.classList.add('drop-target');
            }
        });

        overlay.style.display = 'block';
    }

    generateDropZones(activeGroup) {
        const count = activeGroup.paneIds.length;
        const ratio = activeGroup.splitRatio || [0.5, 0.5];

        // Get label for a pane position (using cellMapping to find the pane)
        const cm = activeGroup.cellMapping || activeGroup.paneIds.map((_, i) => i);
        const getLabel = (panePosition) => {
            const paneIdx = cm[panePosition];
            const paneId = activeGroup.paneIds[paneIdx];
            const pane = this.panes.get(paneId);
            return pane ? this.getPaneDisplayName(pane) : `Pane ${paneIdx + 1}`;
        };
        const visibleLabel = panePosition => this.escapeHtml(getLabel(panePosition));
        const attributeLabel = panePosition => this.escapeHtmlAttribute(getLabel(panePosition));

        if (count === 0) {
            return `<div class="drop-zone drop-full" data-position="center" data-index="0" role="button" aria-label="Drop here to create first pane">Drop here</div>`;
        }

        if (count === 1) {
            return `
                <div class="drop-zone drop-edge drop-top" data-position="top" data-index="0" role="button" aria-label="Drop above ${attributeLabel(0)}">Above "${visibleLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-bottom" data-position="bottom" data-index="1" role="button" aria-label="Drop below ${attributeLabel(0)}">Below "${visibleLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-left" data-position="left" data-index="0" role="button" aria-label="Drop left of ${attributeLabel(0)}">Left of "${visibleLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-right" data-position="right" data-index="1" role="button" aria-label="Drop right of ${attributeLabel(0)}">Right of "${visibleLabel(0)}"</div>
            `;
        }

        if (count === 2) {
            const layout = activeGroup.layout;
            const r = ratio[0];
            // Show drop zones based on current layout
            // New pane is always small, splitting the hovered pane.
            // The other pane becomes the wide pane.
            if (layout === 'horizontal') {
                // Two panes side by side [left=0, right=1]
                // Dropping on left side: right becomes wide (right-wide)
                // Dropping on right side: left becomes wide (left-wide)
                const leftPct = r * 100;
                return `
                    <div class="drop-zone drop-half" style="left: 0; top: 0; width: ${leftPct}%; height: 50%;" data-position="split-above-0" data-split-target="0" role="button" aria-label="Drop above ${attributeLabel(0)}">Above "${visibleLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 0; top: 50%; width: ${leftPct}%; height: 50%;" data-position="split-below-0" data-split-target="0" role="button" aria-label="Drop below ${attributeLabel(0)}">Below "${visibleLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: ${leftPct}%; top: 0; width: ${100 - leftPct}%; height: 50%;" data-position="split-above-1" data-split-target="1" role="button" aria-label="Drop above ${attributeLabel(1)}">Above "${visibleLabel(1)}"</div>
                    <div class="drop-zone drop-half" style="left: ${leftPct}%; top: 50%; width: ${100 - leftPct}%; height: 50%;" data-position="split-below-1" data-split-target="1" role="button" aria-label="Drop below ${attributeLabel(1)}">Below "${visibleLabel(1)}"</div>
                `;
            } else {
                // Two panes stacked [top=0, bottom=1]
                // Dropping on top side: bottom becomes wide (bottom-wide)
                // Dropping on bottom side: top becomes wide (top-wide)
                const topPct = r * 100;
                return `
                    <div class="drop-zone drop-half" style="left: 0; top: 0; width: 50%; height: ${topPct}%;" data-position="split-left-0" data-split-target="0" role="button" aria-label="Drop left of ${attributeLabel(0)}">Left of "${visibleLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 50%; top: 0; width: 50%; height: ${topPct}%;" data-position="split-right-0" data-split-target="0" role="button" aria-label="Drop right of ${attributeLabel(0)}">Right of "${visibleLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 0; top: ${topPct}%; width: 50%; height: ${100 - topPct}%;" data-position="split-left-1" data-split-target="1" role="button" aria-label="Drop left of ${attributeLabel(1)}">Left of "${visibleLabel(1)}"</div>
                    <div class="drop-zone drop-half" style="left: 50%; top: ${topPct}%; width: 50%; height: ${100 - topPct}%;" data-position="split-right-1" data-split-target="1" role="button" aria-label="Drop right of ${attributeLabel(1)}">Right of "${visibleLabel(1)}"</div>
                `;
            }
        }

        if (count === 3) {
            const colPct = ratio[0] * 100;
            const rowPct = ratio[1] * 100;

            // Get current expansion direction
            let expandDir = activeGroup.expandedQuadrant;
            if (expandDir === 2 || expandDir === undefined || expandDir === null) expandDir = 'bottom';
            if (expandDir === 1) expandDir = 'right';

            // Create 4 drop zones for all quadrants
            // Label based on which part of the wide pane or which small pane
            const zones = [];

            // Quadrant positions: top-left, top-right, bottom-left, bottom-right
            const quadrants = [
                { pos: 'top-left', style: `left: 0; top: 0; width: ${colPct}%; height: ${rowPct}%;` },
                { pos: 'top-right', style: `left: ${colPct}%; top: 0; width: ${100 - colPct}%; height: ${rowPct}%;` },
                { pos: 'bottom-left', style: `left: 0; top: ${rowPct}%; width: ${colPct}%; height: ${100 - rowPct}%;` },
                { pos: 'bottom-right', style: `left: ${colPct}%; top: ${rowPct}%; width: ${100 - colPct}%; height: ${100 - rowPct}%;` }
            ];

            // Determine which quadrants are part of the wide pane vs small panes
            // bottom-wide: wide spans bottom-left + bottom-right, smalls are top-left, top-right
            // top-wide: wide spans top-left + top-right, smalls are bottom-left, bottom-right
            // right-wide: wide spans top-right + bottom-right, smalls are top-left, bottom-left
            // left-wide: wide spans top-left + bottom-left, smalls are top-right, bottom-right

            let wideQuads, smallQuads;
            if (expandDir === 'bottom') {
                wideQuads = ['bottom-left', 'bottom-right'];
                smallQuads = ['top-left', 'top-right'];
            } else if (expandDir === 'top') {
                wideQuads = ['top-left', 'top-right'];
                smallQuads = ['bottom-left', 'bottom-right'];
            } else if (expandDir === 'right') {
                wideQuads = ['top-right', 'bottom-right'];
                smallQuads = ['top-left', 'bottom-left'];
            } else { // left
                wideQuads = ['top-left', 'bottom-left'];
                smallQuads = ['top-right', 'bottom-right'];
            }

            for (const q of quadrants) {
                const isWide = wideQuads.includes(q.pos);
                const label = isWide ? `Split wide (${q.pos.replace('-', ' ')})` : `Replace ${q.pos.replace('-', ' ')}`;
                zones.push(`<div class="drop-zone drop-quad" style="${q.style}" data-position="${q.pos}" data-expand-dir="${expandDir}" role="button" aria-label="${this.escapeHtml(label)}">${label}</div>`);
            }

            return zones.join('');
        }

        return '';
    }

    hideDragOverlay() {
        const overlay = document.getElementById('drag-capture-overlay');
        if (overlay) {
            overlay.style.display = 'none';
            overlay.querySelectorAll('.drop-zone').forEach(zone => {
                zone.classList.remove('drag-over');
            });
        }

        document.querySelectorAll('.pane-container.drop-target').forEach(el => {
            el.classList.remove('drop-target');
        });
    }

    // Sidebar Reordering
    // ==================

    updateSidebarDropIndicator(container, e) {
        const rect = container.getBoundingClientRect();
        const midY = rect.top + rect.height / 2;

        this.clearSidebarDropIndicators();

        if (e.clientY < midY) {
            container.classList.add('drop-above');
        } else {
            container.classList.add('drop-below');
        }
    }

    clearSidebarDropIndicators() {
        document.querySelectorAll('.group-container').forEach(el => {
            el.classList.remove('drop-above', 'drop-below');
        });
    }

    handleSidebarDrop(container, targetGroupId, e) {
        this.clearSidebarDropIndicators();

        const rect = container.getBoundingClientRect();
        const dropAbove = e.clientY < rect.top + rect.height / 2;

        let sourceGroupId = this.draggedGroupId;
        if (this.draggedPaneId) {
            for (const [gid, g] of this.groups) {
                if (g.paneIds.includes(this.draggedPaneId)) {
                    sourceGroupId = gid;
                    break;
                }
            }
        }

        if (!sourceGroupId || sourceGroupId === targetGroupId) return;

        this.reorderGroup(sourceGroupId, targetGroupId, dropAbove);
    }

    reorderGroup(sourceGroupId, targetGroupId, dropAbove) {
        const sourceIdx = this.groupOrder.indexOf(sourceGroupId);
        const targetIdx = this.groupOrder.indexOf(targetGroupId);

        if (sourceIdx === -1 || targetIdx === -1) return;

        this.groupOrder.splice(sourceIdx, 1);

        let newIdx = this.groupOrder.indexOf(targetGroupId);
        if (!dropAbove) newIdx++;

        this.groupOrder.splice(newIdx, 0, sourceGroupId);

        this.rerenderSidebarOrder();
        this.refreshPaneTitles();
        this.saveUIState();
    }

    rerenderSidebarOrder() {
        const paneList = this.paneList;

        for (const groupId of this.groupOrder) {
            const container = document.getElementById(`group-${groupId}`);
            if (container) {
                paneList.appendChild(container);
            }
        }
    }

    handleSplitDrop(e) {
        const dropZone = e.target.closest('.drop-zone');
        if (!dropZone) return;

        const position = dropZone.dataset.position;
        const dropIndex = parseInt(dropZone.dataset.index) || 0;
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.paneIds.length >= 4) return;

        const draggedPaneId = this.draggedPaneId;
        if (!draggedPaneId || activeGroup.paneIds.includes(draggedPaneId)) return;

        for (const [gid, g] of this.groups) {
            if (this.removePaneFromGroup(g, draggedPaneId)) {
                if (g.paneIds.length === 0) {
                    this.groups.delete(gid);
                    this.groupOrder = this.groupOrder.filter(id => id !== gid);
                    document.getElementById(`group-${gid}`)?.remove();
                } else {
                    this.updateGroupLayout(g);
                    this.updateGroupInSidebar(g);
                }
                break;
            }
        }

        const currentCount = activeGroup.paneIds.length;

        if (currentCount === 0 || position === 'center') {
            activeGroup.paneIds.push(draggedPaneId);
            activeGroup.layout = 'single';
            activeGroup.splitRatio = null;
        } else if (currentCount === 1) {
            // Always append to maintain insertion order
            activeGroup.paneIds.push(draggedPaneId);
            activeGroup.layout = (position === 'left' || position === 'right') ? 'horizontal' : 'vertical';
            activeGroup.splitRatio = [0.5];
            // Set cellMapping based on drop position
            if (position === 'left' || position === 'top') {
                // New pane (index 1) goes in pane 0, original (index 0) goes in pane 1
                activeGroup.cellMapping = [1, 0];
            } else {
                // Original (index 0) in pane 0, new (index 1) in pane 1
                activeGroup.cellMapping = [0, 1];
            }
        } else if (currentCount === 2) {
            // Handle 2->3 pane transition
            // New pane is always small, other pane becomes wide.
            // paneIds stays in insertion order, cellMapping determines visual positions
            const currentLayout = activeGroup.layout;
            const cm = activeGroup.cellMapping || [0, 1]; // current cell mapping

            // New pane is always appended (index 2)
            activeGroup.paneIds.push(draggedPaneId);
            const newIdx = 2;

            if (position.startsWith('split-')) {
                // Parse position: split-{above|below|left|right}-{targetIdx}
                const parts = position.split('-');
                const splitDir = parts[1]; // above, below, left, right
                const targetPaneIdx = parseInt(parts[2]); // which pane to split (0 or 1)
                const targetPaneIndex = cm[targetPaneIdx]; // pane index in that pane
                const otherPaneIndex = cm[1 - targetPaneIdx]; // the other pane

                // 3-pane cell positions:
                // bottom-wide: [pane0=top-left, pane1=top-right, pane2=wide-bottom]
                // top-wide: [pane0=wide-top, pane1=bottom-left, pane2=bottom-right]
                // right-wide: [pane0=top-left, pane1=wide-right, pane2=bottom-left]
                // left-wide: [pane0=wide-left, pane1=top-right, pane2=bottom-right]

                if (currentLayout === 'horizontal') {
                    // Horizontal: pane0=left, pane1=right -> splitting creates vertical stack on one side
                    if (targetPaneIdx === 0) {
                        // Splitting left pane, right becomes wide (right-wide)
                        if (splitDir === 'above') {
                            // new on top-left, target on bottom-left, other stays wide-right
                            activeGroup.cellMapping = [newIdx, otherPaneIndex, targetPaneIndex];
                        } else {
                            // target on top-left, new on bottom-left, other stays wide-right
                            activeGroup.cellMapping = [targetPaneIndex, otherPaneIndex, newIdx];
                        }
                        activeGroup.expandedQuadrant = 'right';
                    } else {
                        // Splitting right pane, left becomes wide (left-wide)
                        if (splitDir === 'above') {
                            // other stays wide-left, new on top-right, target on bottom-right
                            activeGroup.cellMapping = [otherPaneIndex, newIdx, targetPaneIndex];
                        } else {
                            // other stays wide-left, target on top-right, new on bottom-right
                            activeGroup.cellMapping = [otherPaneIndex, targetPaneIndex, newIdx];
                        }
                        activeGroup.expandedQuadrant = 'left';
                    }
                } else {
                    // Vertical: pane0=top, pane1=bottom -> splitting creates horizontal pair on one side
                    if (targetPaneIdx === 0) {
                        // Splitting top pane, bottom becomes wide (bottom-wide)
                        if (splitDir === 'left') {
                            // new on top-left, target on top-right, other stays wide-bottom
                            activeGroup.cellMapping = [newIdx, targetPaneIndex, otherPaneIndex];
                        } else {
                            // target on top-left, new on top-right, other stays wide-bottom
                            activeGroup.cellMapping = [targetPaneIndex, newIdx, otherPaneIndex];
                        }
                        activeGroup.expandedQuadrant = 'bottom';
                    } else {
                        // Splitting bottom pane, top becomes wide (top-wide)
                        if (splitDir === 'left') {
                            // other stays wide-top, new on bottom-left, target on bottom-right
                            activeGroup.cellMapping = [otherPaneIndex, newIdx, targetPaneIndex];
                        } else {
                            // other stays wide-top, target on bottom-left, new on bottom-right
                            activeGroup.cellMapping = [otherPaneIndex, targetPaneIndex, newIdx];
                        }
                        activeGroup.expandedQuadrant = 'top';
                    }
                }
            } else {
                // Fallback for old-style positions - just use default bottom-wide layout
                activeGroup.cellMapping = [0, 1, 2];
                activeGroup.expandedQuadrant = 'bottom';
            }

            activeGroup.layout = 'grid';
            activeGroup.splitRatio = [0.5, 0.5];
        } else if (currentCount === 3) {
            // 3->4 pane transition
            // User drops into one of four quadrants
            // paneIds stays in insertion order, cellMapping determines visual positions

            // Append new pane (always at index 3)
            activeGroup.paneIds.push(draggedPaneId);
            const newIdx = 3;

            // Get current cell mapping (maps pane position -> pane index)
            const cm = activeGroup.cellMapping || [0, 1, 2];

            let expandDir = activeGroup.expandedQuadrant;
            if (expandDir === 2 || expandDir === undefined || expandDir === null) expandDir = 'bottom';
            if (expandDir === 1) expandDir = 'right';

            // 3-pane cell positions by expandDir:
            // bottom-wide: [pane0=top-left, pane1=top-right, pane2=wide-bottom]
            // top-wide: [pane0=wide-top, pane1=bottom-left, pane2=bottom-right]
            // right-wide: [pane0=top-left, pane1=wide-right, pane2=bottom-left]
            // left-wide: [pane0=wide-left, pane1=top-right, pane2=bottom-right]

            // 4-pane target: [pane0=top-left, pane1=top-right, pane2=bottom-left, pane3=bottom-right]

            const targetQuad = position; // top-left, top-right, bottom-left, bottom-right
            const quadToPane = { 'top-left': 0, 'top-right': 1, 'bottom-left': 2, 'bottom-right': 3 };
            const targetPane = quadToPane[targetQuad];

            // Determine which quadrants the wide pane occupies in 3-pane layout
            let wideQuads, widePaneIdx;
            if (expandDir === 'bottom') {
                wideQuads = ['bottom-left', 'bottom-right'];
                widePaneIdx = 2;
            } else if (expandDir === 'top') {
                wideQuads = ['top-left', 'top-right'];
                widePaneIdx = 0;
            } else if (expandDir === 'right') {
                wideQuads = ['top-right', 'bottom-right'];
                widePaneIdx = 1;
            } else { // left
                wideQuads = ['top-left', 'bottom-left'];
                widePaneIdx = 0;
            }

            const widePaneIndex = cm[widePaneIdx];
            const isDropOnWide = wideQuads.includes(targetQuad);

            // Build new 4-pane cellMapping
            let newCm = [null, null, null, null];

            if (isDropOnWide) {
                // Splitting the wide pane - new pane goes to target, wide stays in other half.
                const otherWideQuad = wideQuads.find(q => q !== targetQuad);
                const otherWidePane = quadToPane[otherWideQuad];

                newCm[targetPane] = newIdx;
                newCm[otherWidePane] = widePaneIndex;

                // Place the two small panes in their current visual spots
                if (expandDir === 'bottom') {
                    newCm[0] = cm[0]; // top-left stays
                    newCm[1] = cm[1]; // top-right stays
                } else if (expandDir === 'top') {
                    newCm[2] = cm[1]; // bottom-left stays
                    newCm[3] = cm[2]; // bottom-right stays
                } else if (expandDir === 'right') {
                    newCm[0] = cm[0]; // top-left stays
                    newCm[2] = cm[2]; // bottom-left stays
                } else { // left
                    newCm[1] = cm[1]; // top-right stays
                    newCm[3] = cm[2]; // bottom-right stays
                }
            } else {
                // Dropping on a small pane - move wide pane to opposite side
                let wideNewQuad;
                if (expandDir === 'bottom' || expandDir === 'top') {
                    wideNewQuad = targetQuad.includes('left')
                        ? wideQuads.find(q => q.includes('right'))
                        : wideQuads.find(q => q.includes('left'));
                } else {
                    wideNewQuad = targetQuad.includes('top')
                        ? wideQuads.find(q => q.includes('bottom'))
                        : wideQuads.find(q => q.includes('top'));
                }

                const wideNewPane = quadToPane[wideNewQuad];
                const otherWideQuad = wideQuads.find(q => q !== wideNewQuad);
                const otherWidePane = quadToPane[otherWideQuad];

                // New pane at target
                newCm[targetPane] = newIdx;
                // Wide pane moves
                newCm[wideNewPane] = widePaneIndex;

                // Get current small pane positions
                let small1Pane, small2Pane, small1Quad, small2Quad;
                if (expandDir === 'bottom') {
                    small1Pane = 0; small1Quad = 'top-left';
                    small2Pane = 1; small2Quad = 'top-right';
                } else if (expandDir === 'top') {
                    small1Pane = 1; small1Quad = 'bottom-left';
                    small2Pane = 2; small2Quad = 'bottom-right';
                } else if (expandDir === 'right') {
                    small1Pane = 0; small1Quad = 'top-left';
                    small2Pane = 2; small2Quad = 'bottom-left';
                } else {
                    small1Pane = 1; small1Quad = 'top-right';
                    small2Pane = 2; small2Quad = 'bottom-right';
                }

                // Small that was at target moves to freed wide spot
                // Other small stays where it was
                if (targetQuad === small1Quad) {
                    newCm[otherWidePane] = cm[small1Pane];
                    newCm[quadToPane[small2Quad]] = cm[small2Pane];
                } else {
                    newCm[otherWidePane] = cm[small2Pane];
                    newCm[quadToPane[small1Quad]] = cm[small1Pane];
                }
            }

            activeGroup.cellMapping = newCm;
            activeGroup.layout = 'grid';
            activeGroup.expandedQuadrant = null; // 4-pane has no expanded quadrant
        }

        this.refreshPaneTitles();
        this.updatePaneLayout();
        this.hideDragOverlay();
        this.draggedPaneId = null;
        this.saveUIState();
    }

    // Modals & Utilities
    // ==================

    toggleSidebar() {
        this.sidebar.classList.toggle('collapsed');
        if (this.sidebar.classList.contains('collapsed')) {
            this.startIconFadeTimer();
        } else {
            this.clearIconFade();
        }
        this.saveUIState();
        this.scheduleSharedIframePosition();
    }

    openModal(modal) {
        modal.classList.remove('hidden');
        if (modal === this.downloadModal) {
            // Wait for modal to render before updating UI (for accurate height)
            requestAnimationFrame(() => this.updateMarkedUI());
        }
    }

    closeModal(modal) {
        modal.classList.add('hidden');
        if (modal === this.uploadModal) {
            this.uploadProgress.classList.add('hidden');
            this.uploadResults.classList.add('hidden');
        }
        if (modal === this.downloadModal) {
            this.markedSidekick.classList.add('hidden');
        }
    }

    // Logs Modal
    // ==========

    openLogsModal() {
        this.openModal(this.logsModal);
        this.fetchLogs();
        if (this.logsAutoRefresh.checked) {
            this.startLogsAutoRefresh();
        }
    }

    closeLogsModal() {
        this.stopLogsAutoRefresh();
        this.logsFetchPending = false; // Cancel any pending fetch display
        this.closeModal(this.logsModal);
    }

    async fetchLogs() {
        // Prevent concurrent fetches
        if (this.logsFetchPending) return;
        this.logsFetchPending = true;

        try {
            const response = await fetch(this.url('/api/logs'));

            // Check if modal was closed while we were fetching
            if (this.logsModal.classList.contains('hidden')) {
                return;
            }

            if (!response.ok) {
                throw new Error(`HTTP ${response.status}`);
            }
            const logs = await response.text();

            // Only update if content changed (preserves user's text selection)
            if (this.logsContent.textContent === logs) {
                return;
            }

            const wasAtBottom = this.isLogsScrolledToBottom();
            // textContent is safe from XSS
            this.logsContent.textContent = logs;
            // Auto-scroll to bottom if user was already at bottom
            if (wasAtBottom) {
                this.scrollLogsToBottom();
            }
        } catch (error) {
            // Only show error if modal is still open
            if (!this.logsModal.classList.contains('hidden')) {
                console.error('Failed to fetch logs:', error);
                this.logsContent.textContent = `Failed to load logs: ${error.message}`;
            }
        } finally {
            this.logsFetchPending = false;
        }
    }

    isLogsScrolledToBottom() {
        const el = this.logsContent;
        // Consider "at bottom" if within 50px of the bottom
        return el.scrollHeight - el.scrollTop - el.clientHeight < 50;
    }

    scrollLogsToBottom() {
        this.logsContent.scrollTop = this.logsContent.scrollHeight;
    }

    startLogsAutoRefresh() {
        this.stopLogsAutoRefresh();
        if (this.connectionMode !== 'active') return;
        const es = new EventSource(this.url('/api/logs/events'));
        let refreshTimer = null;

        es.onmessage = (event) => {
            if (event.data === 'init') return;
            if (this.logsModal.classList.contains('hidden')) return;
            clearTimeout(refreshTimer);
            refreshTimer = setTimeout(() => this.fetchLogs(), 250);
        };
        es.onerror = () => {
            es.close();
            if (!this.logsModal.classList.contains('hidden') && this.logsAutoRefresh.checked) {
                this.logsRefreshInterval = setTimeout(() => this.startLogsAutoRefresh(), 2000);
            }
        };
        this.logsRefreshInterval = {
            close: () => {
                clearTimeout(refreshTimer);
                es.close();
            }
        };
    }

    stopLogsAutoRefresh() {
        if (this.logsRefreshInterval) {
            if (typeof this.logsRefreshInterval.close === 'function') {
                this.logsRefreshInterval.close();
            } else {
                clearTimeout(this.logsRefreshInterval);
            }
            this.logsRefreshInterval = null;
        }
    }

    // File Upload/Download
    // ====================

    handleFileSelect(event) {
        const files = event.target.files;
        if (files.length > 0) this.uploadFiles(files);
    }

    handleFileDrop(event) {
        const files = event.dataTransfer.files;
        if (files.length > 0) this.uploadFiles(files);
    }

    async uploadFiles(files) {
        const formData = new FormData();
        for (const file of files) formData.append('files', file);

        const directory = this.uploadDirectory.value.trim();
        if (directory) formData.append('directory', directory);

        this.uploadProgress.classList.remove('hidden');
        this.uploadResults.classList.add('hidden');
        const progressFill = this.uploadProgress.querySelector('.progress-fill');
        const progressText = this.uploadProgress.querySelector('.progress-text');

        progressFill.style.width = '0%';
        progressText.textContent = 'Uploading...';

        try {
            const response = await fetch(this.url('/api/upload'), { method: 'POST', body: formData });
            if (!response.ok) {
                const text = await response.text();
                throw new Error(`Upload failed (${response.status}): ${text}`);
            }

            const result = await response.json();
            progressFill.style.width = '100%';
            progressText.textContent = 'Complete!';

            this.uploadResults.classList.remove('hidden');
            this.uploadResults.innerHTML = `
                <p>Successfully uploaded ${result.count} file(s):</p>
                <ul style="margin-top: 8px; padding-left: 20px; color: var(--text-secondary);">
                    ${result.uploaded.map(f => `<li style="font-family: monospace; font-size: 13px;">${this.escapeHtml(f)}</li>`).join('')}
                </ul>
            `;
        } catch (error) {
            console.error('Upload failed:', error);
            progressText.textContent = 'Upload failed!';
            progressFill.style.background = 'var(--danger)';
        }

        this.fileInput.value = '';
    }

    // SECTION: FILES

    async browsePath(path) {
        try {
            const response = await fetch(this.url(`/api/browse?path=${encodeURIComponent(path)}`));
            if (!response.ok) throw new Error('Failed to browse directory');

            const result = await response.json();
            this.currentPathInput.value = result.path;
            this.currentFiles = result.files;
            this.updateSortIndicators();
            this.renderFileList();
        } catch (error) {
            console.error('Failed to browse:', error);
            this.fileList.innerHTML = `<p style="padding: 20px; color: var(--danger);">Failed to load directory</p>`;
            this.fileCountEl.textContent = '';
        }
    }

    updateSortIndicators() {
        this.fileHeader.querySelectorAll('.sortable').forEach(col => {
            const isActive = col.dataset.sort === this.fileSortBy;
            col.classList.toggle('active', isActive);
            col.classList.toggle('asc', isActive && this.fileSortAsc);
        });
    }

    sortFiles(files) {
        // Separate parent dir (..), directories, and files
        const parentDir = files.filter(f => f.name === '..');
        const dirs = files.filter(f => f.isDir && f.name !== '..');
        const regularFiles = files.filter(f => !f.isDir);

        // Sort function based on current sort settings
        const sortFn = (a, b) => {
            let cmp = 0;
            switch (this.fileSortBy) {
                case 'name':
                    // Case-sensitive sort (uppercase before lowercase in ASCII)
                    cmp = a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
                    break;
                case 'size':
                    cmp = (a.size || 0) - (b.size || 0);
                    break;
                case 'date':
                    cmp = (a.modTime || 0) - (b.modTime || 0);
                    break;
            }
            return this.fileSortAsc ? cmp : -cmp;
        };

        // Sort directories and files separately
        dirs.sort(sortFn);
        regularFiles.sort(sortFn);

        // Return: parent first, then directories, then files
        return [...parentDir, ...dirs, ...regularFiles];
    }

    renderFileList() {
        const files = this.sortFiles(this.currentFiles);
        const markedPaths = new Set(this.markedFiles.map(f => f.path));

        // Update file count
        const dirCount = files.filter(f => f.isDir && f.name !== '..').length;
        const fileCount = files.filter(f => !f.isDir).length;
        this.fileCountEl.textContent = `${dirCount} folder${dirCount !== 1 ? 's' : ''}, ${fileCount} file${fileCount !== 1 ? 's' : ''}`;

        this.fileList.innerHTML = files.map(file => {
            const isMarked = markedPaths.has(file.path);
            const isParent = file.name === '..';
            const canMark = file.isDir || file.isRegular; // Directories and regular files can be marked
            // Bookmark icon for mark, filled when marked
            const markIcon = isMarked
                ? '<path fill="currentColor" d="M17 3H7c-1.1 0-2 .9-2 2v16l7-3 7 3V5c0-1.1-.9-2-2-2z"/>'
                : '<path fill="currentColor" d="M17 3H7c-1.1 0-2 .9-2 2v16l7-3 7 3V5c0-1.1-.9-2-2-2zm0 15l-5-2.18L7 18V5h10v13z"/>';
            // Mark button - shown for all except parent dir, disabled for non-regular files
            const markBtn = isParent ? '' : `
                <button class="action-btn mark-btn ${isMarked ? 'marked' : ''} ${!canMark ? 'disabled' : ''}"
                        title="${!canMark ? 'Cannot mark this file type' : (isMarked ? 'Unmark' : 'Mark for download')}"
                        aria-label="${isMarked ? 'Unmark' : 'Mark'} ${this.escapeHtml(file.name)} for download"
                        ${!canMark ? 'disabled' : ''}>
                    <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">${markIcon}</svg>
                </button>
            `;
            return `
            <div class="file-item ${file.isDir ? 'directory' : ''}" data-path="${this.escapeHtml(file.path)}" data-is-dir="${file.isDir}" data-is-regular="${!!file.isRegular}"
                 role="row" aria-label="${file.isDir ? 'Directory' : 'File'}: ${this.escapeHtml(file.name)}">
                ${markBtn}
                <svg class="icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
                    ${file.isDir
                        ? '<path fill="currentColor" d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/>'
                        : '<path fill="currentColor" d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/>'}
                </svg>
                <span class="name" role="gridcell">${this.escapeHtml(file.name)}</span>
                <span class="size" role="gridcell">${file.isDir ? (isParent ? '—' : `${file.size} item${file.size !== 1 ? 's' : ''}`) : this.formatSize(file.size)}</span>
                <span class="modified" role="gridcell">${file.modTime ? this.formatDate(file.modTime) : '—'}</span>
                <span class="actions" role="gridcell">
                    ${(file.isRegular || file.isDir) && !isParent ? `
                        <button class="action-btn download-btn" title="${file.isDir ? 'Download as zip' : 'Download'}" aria-label="Download ${this.escapeHtml(file.name)}">
                            <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                                <path fill="currentColor" d="M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z"/>
                            </svg>
                        </button>
                    ` : ''}
                </span>
            </div>
        `}).join('');

        this.fileList.querySelectorAll('.file-item').forEach(item => {
            const path = item.dataset.path;
            const isDir = item.dataset.isDir === 'true';
            const isRegular = item.dataset.isRegular === 'true';
            const canMark = isDir || isRegular;

            // Get file data for this item
            const file = files.find(f => f.path === path);

            // Directories: click to navigate
            if (isDir) {
                item.addEventListener('click', () => this.browsePath(path));
            } else if (file) {
                // Files: click to show info popup
                item.addEventListener('click', (e) => {
                    this.showFileInfoPopup(file, e);
                });
            }

            // Mark button for directories and regular files
            if (canMark) {
                const markBtn = item.querySelector('.mark-btn');
                if (markBtn) {
                    markBtn.addEventListener('click', (e) => {
                        e.stopPropagation();
                        if (this.isFileMarked(path)) {
                            this.unmarkFile(path);
                        } else {
                            this.markFile(path);
                        }
                    });
                }
            }

            // Download button for regular files and directories
            const downloadBtn = item.querySelector('.download-btn');
            if (downloadBtn) {
                downloadBtn.addEventListener('click', (e) => {
                    e.stopPropagation();
                    window.open(this.url(`/api/download?path=${encodeURIComponent(path)}`), '_blank');
                });
            }
        });

        // Re-constrain sidekick height after file list renders (modal height may have changed)
        if (!this.markedSidekick.classList.contains('hidden')) {
            requestAnimationFrame(() => this.constrainSidekickHeight());
        }
    }

    formatDate(timestamp) {
        const date = new Date(timestamp * 1000);
        const now = new Date();
        const diffDays = Math.floor((now - date) / (1000 * 60 * 60 * 24));

        if (diffDays === 0) {
            return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        } else if (diffDays < 7) {
            return date.toLocaleDateString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
        } else {
            return date.toLocaleDateString([], { month: 'short', day: 'numeric', year: date.getFullYear() !== now.getFullYear() ? 'numeric' : undefined });
        }
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    escapeHtmlAttribute(text) {
        return this.escapeHtml(text).replaceAll('"', '&quot;').replaceAll("'", '&#39;');
    }

    formatSize(bytes) {
        if (bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
    }

    // File Info Popup
    // ===============

    showFileInfoPopup(file, event) {
        this.currentFileInfo = file;

        // Update popup content
        this.fileInfoName.textContent = file.name;
        this.fileInfoPath.textContent = file.path;
        this.fileInfoSize.textContent = this.formatSize(file.size);
        this.fileInfoModified.textContent = file.modTime
            ? new Date(file.modTime * 1000).toLocaleString()
            : '—';

        // Update icon for directory
        const isDir = file.isDir;
        this.fileInfoPopup.classList.toggle('directory', isDir);
        this.fileInfoIcon.innerHTML = isDir
            ? '<path fill="currentColor" d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/>'
            : '<path fill="currentColor" d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/>';

        // Position popup near the clicked item
        const rect = event.currentTarget.getBoundingClientRect();
        const popup = this.fileInfoPopup;

        // Show popup to measure dimensions
        popup.classList.remove('hidden');
        const popupRect = popup.getBoundingClientRect();

        // Position to the right of the item, or left if not enough space
        let left = rect.right + 8;
        let top = rect.top;

        // Keep within viewport
        if (left + popupRect.width > window.innerWidth - 16) {
            left = rect.left - popupRect.width - 8;
        }
        if (top + popupRect.height > window.innerHeight - 16) {
            top = window.innerHeight - popupRect.height - 16;
        }
        if (top < 16) top = 16;

        popup.style.left = `${left}px`;
        popup.style.top = `${top}px`;
    }

    hideFileInfoPopup() {
        this.fileInfoPopup.classList.add('hidden');
        this.currentFileInfo = null;
    }

    copyFileInfoPath() {
        if (!this.currentFileInfo) return;
        const btn = this.fileInfoCopyBtn;
        const originalHTML = btn.innerHTML;

        navigator.clipboard.writeText(this.currentFileInfo.path).then(() => {
            // Show success state
            btn.innerHTML = `
                <svg viewBox="0 0 24 24" width="14" height="14">
                    <path fill="currentColor" d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/>
                </svg>
                Copied!
            `;
            btn.classList.add('success');

            // Reset after delay then close
            setTimeout(() => {
                btn.innerHTML = originalHTML;
                btn.classList.remove('success');
                this.hideFileInfoPopup();
            }, 800);
        }).catch(err => {
            this.showToast('Failed to copy path', 'error');
        });
    }

    async sendFileInfoToScratch() {
        if (!this.currentFileInfo) return;
        try {
            // Get current scratch content
            const response = await fetch(this.url('/api/scratch'));
            const data = await response.json();
            const currentText = data.text || '';

            // Append path on new line
            const newText = currentText
                ? currentText + '\n' + this.currentFileInfo.path
                : this.currentFileInfo.path;

            // Save back
            await fetch(this.url('/api/scratch'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ text: newText })
            });

            this.showToast('Path added to scratch pad');
            this.hideFileInfoPopup();
        } catch (err) {
            this.showToast('Failed to add to scratch pad', 'error');
        }
    }

    // SECTION: SETTINGS

    // Settings
    // ========

    async loadSettings() {
        try {
            const response = await fetch(this.url('/api/settings'));
            this.settings = await response.json();
            this.applyUIColors(this.settings.ui);
            this.renderKeybar();
            this.renderMobileKeybar();
        } catch (error) {
            console.error('Failed to load settings:', error);
            // Use defaults
            this.settings = this.getDefaultSettings();
            this.renderKeybar();
            this.renderMobileKeybar();
        }
    }

    async loadServerInfo() {
        try {
            const response = await fetch(this.url('/api/info'));
            const info = await response.json();
            if (this.observeServerIdentity(info.serverRunId, info.assetVersion)) return;
            this.serverInfo = info;
            if (Array.isArray(info.paneTypes)) {
                this.paneTypes = info.paneTypes;
                this.renderPaneTypeMenus();
            }

            // Set upload directory placeholder
            if (this.uploadDirectory && info.uploadDir) {
                this.uploadDirectory.placeholder = info.uploadDir;
            }
        } catch (error) {
            console.error('Failed to load server info:', error);
        }
    }

    getDefaultSettings() {
        return {
            panes: {
                attentionIndicators: true,
                showAttentionInTitle: true,
                playAttentionSound: true,
                terminal: { indicateAttention: true },
                opencode: { indicateAttention: true }
            },
            ui: {
                bgPrimary: '#1e1e2e',
                bgSecondary: '#181825',
                bgTertiary: '#313244',
                textPrimary: '#cdd6f4',
                textSecondary: '#a6adc8',
                textMuted: '#6c7086',
                accent: '#89b4fa',
                accentHover: '#b4befe',
                border: '#45475a'
            },
            terminal: {
                base00: '#1e1e2e', // Background
                base01: '#181825', // Lighter Background
                base02: '#313244', // Selection
                base03: '#45475a', // Comments
                base04: '#585b70', // Dark Foreground
                base05: '#cdd6f4', // Foreground
                base06: '#f5e0dc', // Light Foreground
                base07: '#ffffff', // Lightest
                base08: '#f38ba8', // Red
                base09: '#fab387', // Orange
                base0A: '#f9e2af', // Yellow
                base0B: '#a6e3a1', // Green
                base0C: '#94e2d5', // Cyan
                base0D: '#89b4fa', // Blue
                base0E: '#cba6f7', // Magenta
                base0F: '#f2cdcd', // Brown
                base10: '#11111b', // Darker Background
                base11: '#0a0a0f', // Darkest Background
                base12: '#f38ba8', // Bright Red
                base13: '#f9e2af', // Bright Yellow
                base14: '#a6e3a1', // Bright Green
                base15: '#94e2d5', // Bright Cyan
                base16: '#89b4fa', // Bright Blue
                base17: '#cba6f7'  // Bright Magenta
            },
            keybar: {
                buttons: ['C-c', 'C-d', 'C-z', 'C-\\', 'C-l', 'C-r', 'C-u', 'C-w'],
                anchor: 'bottom'
            },
            diagnostics: {
                enabled: false,
                clientEvents: false,
                proxyWebSockets: false,
                paneEvents: false,
                storageEvents: false,
                iframeLifecycle: false,
                optionalPing: false,
                pingIntervalSeconds: 30
            }
        };
    }

    diagnosticsEnabled(category = 'clientEvents') {
        const diagnostics = this.settings?.diagnostics;
        if (!diagnostics?.enabled) return false;
        return diagnostics[category] === true;
    }

    logDiagnostic(source, event, details = {}) {
        if (!this.diagnosticsEnabled('clientEvents') && !(source === 'iframe' && this.diagnosticsEnabled('iframeLifecycle'))) return;
        const payload = {
            source,
            event,
            paneId: details.paneId || '',
            backendId: details.backendId || '',
            paneType: details.paneType || '',
            path: details.path || '',
            ageMs: details.ageMs || 0,
            data: details.data || {}
        };
        this.diagnosticQueue.push(payload);
        if (this.diagnosticQueue.length > 100) this.diagnosticQueue.splice(0, this.diagnosticQueue.length - 100);
        if (this.diagnosticFlushTimer) return;
        this.diagnosticFlushTimer = setTimeout(() => this.flushDiagnostics(), 500);
    }

    flushDiagnostics() {
        clearTimeout(this.diagnosticFlushTimer);
        this.diagnosticFlushTimer = null;
        if (this.diagnosticQueue.length === 0) return;
        const events = this.diagnosticQueue.splice(0, 50);
        const body = JSON.stringify(events);
        const url = this.url('/api/diagnostics/client');
        if (navigator.sendBeacon) {
            try {
                if (navigator.sendBeacon(url, new Blob([body], { type: 'application/json' }))) return;
            } catch (e) {}
        }
        fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body, keepalive: true }).catch(() => {
            this.diagnosticQueue.unshift(...events);
            if (this.diagnosticQueue.length > 100) this.diagnosticQueue.length = 100;
        });
    }

    validateKeyCombo(keys) {
        // Valid key combinations:
        // - C-x (Ctrl + key)
        // - M-x (Alt/Meta + key)
        // - S-x (Shift + key)
        // - C-M-x (Ctrl + Alt + key)
        // - C-S-x (Ctrl + Shift + key)
        // - M-S-x (Alt + Shift + key)
        // - C-M-S-x (Ctrl + Alt + Shift + key)
        // - F1-F12 (function keys)
        // - Tab, Enter, Escape, Space, Backspace, Delete
        // - Single letters, numbers, symbols
        // - Special actions: Paste (reads system clipboard)

        // Check for special actions first
        if (keys.trim().toLowerCase() === 'paste') {
            return true;
        }

        const keyPattern = /^(?:C-)?(?:M-)?(?:S-)?([A-Za-z0-9]|F[1-9]|F1[0-2]|Tab|Enter|Escape|Esc|Space|Backspace|BS|Delete|Del|Up|Down|Left|Right|Home|End|PageUp|PageDown|PgUp|PgDn|Insert|Ins|[\[\]\/\\.,;:'"`~!@#$%^&*()\-_=+<>|])$/i;
        return keyPattern.test(keys.trim());
    }

    normalizeKeyCombo(keys) {
        // Normalize key combo to canonical form
        let normalized = keys.trim();

        // Normalize modifiers (case-insensitive)
        normalized = normalized.replace(/^c-/i, 'C-');
        normalized = normalized.replace(/^m-/i, 'M-');
        normalized = normalized.replace(/^s-/i, 'S-');
        normalized = normalized.replace(/C-m-/i, 'C-M-');
        normalized = normalized.replace(/C-s-/i, 'C-S-');
        normalized = normalized.replace(/M-s-/i, 'M-S-');
        normalized = normalized.replace(/C-M-s-/i, 'C-M-S-');

        // Extract the key part (after all modifiers)
        const modifierMatch = normalized.match(/^((?:C-)?(?:M-)?(?:S-)?)(.+)$/);
        if (modifierMatch) {
            const modifiers = modifierMatch[1];
            let key = modifierMatch[2];

            // Normalize special key names
            const keyNormalizations = {
                'tab': 'Tab',
                'enter': 'Enter',
                'return': 'Enter',
                'escape': 'Escape',
                'esc': 'Escape',
                'space': 'Space',
                'backspace': 'Backspace',
                'bs': 'Backspace',
                'delete': 'Delete',
                'del': 'Delete',
                'up': 'Up',
                'down': 'Down',
                'left': 'Left',
                'right': 'Right',
                'home': 'Home',
                'end': 'End',
                'pageup': 'PageUp',
                'pgup': 'PageUp',
                'pagedown': 'PageDown',
                'pgdn': 'PageDown',
                'insert': 'Insert',
                'ins': 'Insert',
                'paste': 'Paste',
            };

            const lowerKey = key.toLowerCase();
            if (keyNormalizations[lowerKey]) {
                key = keyNormalizations[lowerKey];
            } else if (/^f([1-9]|1[0-2])$/i.test(key)) {
                // Normalize function keys: f1 -> F1
                key = key.toUpperCase();
            } else if (key.length === 1 && /[a-z]/.test(key)) {
                // Single letter keys stay lowercase
                key = key.toLowerCase();
            }

            normalized = modifiers + key;
        }

        return normalized;
    }

    formatKeyLabel(keys) {
        // Convert key combo to human-readable label
        let label = keys;
        label = label.replace(/^C-/, 'Ctrl-');
        label = label.replace(/^M-/, 'Alt-');
        label = label.replace(/^S-/, 'Shift-');
        label = label.replace(/Ctrl-M-/, 'Ctrl-Alt-');
        label = label.replace(/Ctrl-S-/, 'Ctrl-Shift-');
        label = label.replace(/Alt-S-/, 'Alt-Shift-');
        label = label.replace(/Ctrl-Alt-S-/, 'Ctrl-Alt-Shift-');
        return label;
    }

    formatKeyTitle(keys) {
        // Generate tooltip description based on key combo
        const label = this.formatKeyLabel(keys);
        const descriptions = {
            'C-c': 'Interrupt (SIGINT)',
            'C-d': 'EOF / Exit',
            'C-z': 'Suspend (SIGTSTP)',
            'C-\\': 'Quit (SIGQUIT)',
            'C-l': 'Clear screen',
            'C-r': 'Reverse search history',
            'C-u': 'Clear line',
            'C-w': 'Delete word',
            'C-a': 'Move to beginning of line',
            'C-e': 'Move to end of line',
            'C-k': 'Kill to end of line',
            'C-y': 'Yank (paste)',
            'C-p': 'Previous command',
            'C-n': 'Next command',
            'Tab': 'Tab / Autocomplete',
            'Escape': 'Escape',
            'Paste': 'Paste from system clipboard',
        };
        const desc = descriptions[keys];
        return desc ? `${label}: ${desc}` : label;
    }

    showKeybarInputError(message) {
        const errorEl = document.getElementById('keybar-input-error');
        if (errorEl) {
            errorEl.textContent = message;
            errorEl.classList.remove('hidden');
        }
    }

    hideKeybarInputError() {
        const errorEl = document.getElementById('keybar-input-error');
        if (errorEl) {
            errorEl.classList.add('hidden');
        }
    }

    applyUIColors(ui) {
        const root = document.documentElement;
        root.style.setProperty('--bg-primary', ui.bgPrimary);
        root.style.setProperty('--bg-secondary', ui.bgSecondary);
        root.style.setProperty('--bg-tertiary', ui.bgTertiary);
        root.style.setProperty('--text-primary', ui.textPrimary);
        root.style.setProperty('--text-secondary', ui.textSecondary);
        root.style.setProperty('--text-muted', ui.textMuted);
        root.style.setProperty('--accent', ui.accent);
        root.style.setProperty('--accent-hover', ui.accentHover);
        root.style.setProperty('--border', ui.border);
    }

    updateThemeActionsVisibility(tabName) {
        const themeActions = document.getElementById('settings-theme-actions');
        if (themeActions) {
            themeActions.style.display = (tabName === 'ui' || tabName === 'terminal') ? '' : 'none';
        }
        if (this.settingsConfigActions) {
            this.settingsConfigActions.style.display = tabName === 'storage' ? 'none' : '';
        }
    }

    openSettingsModal() {
        // Populate inputs first, then capture snapshot for comparison
        this.populateSettingsInputs();
        this.originalSettings = JSON.stringify(this.getSettingsSnapshot());
        this.settingsDiscardPending = false;
        this.updateSettingsCloseButton();

        const activeTab = this.settingsModal.querySelector('.settings-tab.active')?.dataset.tab || 'panes';
        this.updateThemeActionsVisibility(activeTab);
        if (activeTab === 'storage') this.loadOpenCodeStorageSummary();

        this.openModal(this.settingsModal);
    }

    getSettingsSnapshot() {
        // Get current settings state from inputs and keybar
        const settings = this.getSettingsFromInputs();
        return settings;
    }

    hasUnsavedSettingsChanges() {
        if (!this.originalSettings) return false;
        const current = JSON.stringify(this.getSettingsSnapshot());
        return current !== this.originalSettings;
    }

    updateSettingsCloseButton() {
        const closeBtn = this.settingsModal.querySelector('.close-modal');
        if (!closeBtn) return;

        if (this.settingsDiscardPending) {
            closeBtn.classList.add('discard-pending');
            closeBtn.innerHTML = `<span class="discard-text">Discard?</span>`;
            closeBtn.title = 'Click again to discard changes';
        } else {
            closeBtn.classList.remove('discard-pending');
            closeBtn.innerHTML = `<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
                <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
            </svg>`;
            closeBtn.title = 'Close';
        }
    }

    handleSettingsClose() {
        if (this.hasUnsavedSettingsChanges()) {
            if (this.settingsDiscardPending) {
                // Second click - actually discard
                this.settingsDiscardPending = false;
                this.discardSettingsChanges();
            } else {
                // First click - show discard confirmation
                this.settingsDiscardPending = true;
                this.updateSettingsCloseButton();
            }
        } else {
            // No changes, just close
            this.closeModal(this.settingsModal);
        }
    }

    resetSettingsDiscardState() {
        if (this.settingsDiscardPending) {
            this.settingsDiscardPending = false;
            this.updateSettingsCloseButton();
        }
    }

    discardSettingsChanges() {
        // Revert to saved settings
        this.applyUIColors(this.settings.ui);
        // Restore keybar settings from saved state
        if (this.originalSettings) {
            const original = JSON.parse(this.originalSettings);
            this.settings.keybar = original.keybar;
        }
        this.closeModal(this.settingsModal);
    }

    closeSettingsModal() {
        this.handleSettingsClose();
    }

    populateSettingsInputs() {
        // Populate UI colors
        for (const [key, value] of Object.entries(this.settings.ui)) {
            const colorInput = this.settingsModal.querySelector(`[data-setting="ui.${key}"]`);
            const hexInput = this.settingsModal.querySelector(`[data-setting-hex="ui.${key}"]`);
            if (colorInput) colorInput.value = value;
            if (hexInput) hexInput.value = value;
        }
        // Populate terminal colors
        for (const [key, value] of Object.entries(this.settings.terminal)) {
            const colorInput = this.settingsModal.querySelector(`[data-setting="terminal.${key}"]`);
            const hexInput = this.settingsModal.querySelector(`[data-setting-hex="terminal.${key}"]`);
            if (colorInput) colorInput.value = value;
            if (hexInput) hexInput.value = value;
        }
        // Populate keybar buttons
        this.populateKeybarButtons();
        const keybarAnchor = this.settings.keybar?.anchor || this.getDefaultSettings().keybar.anchor;
        const keybarAnchorInput = this.settingsModal.querySelector(`input[name="keybar-anchor"][value="${keybarAnchor}"]`);
        if (keybarAnchorInput) keybarAnchorInput.checked = true;

        this.settingsModal.querySelectorAll('[data-setting-bool]').forEach(input => {
            input.checked = this.getSettingValue(this.settings, input.dataset.settingBool)
                ?? this.getSettingValue(this.getDefaultSettings(), input.dataset.settingBool)
                ?? false;
        });
        this.settingsModal.querySelectorAll('[data-setting-number]').forEach(input => {
            input.value = this.getSettingValue(this.settings, input.dataset.settingNumber)
                ?? this.getSettingValue(this.getDefaultSettings(), input.dataset.settingNumber);
        });
        this.syncAttentionSettingsDisabled();
    }

    getSettingValue(settings, path) {
        return path.split('.').reduce((value, key) => value?.[key], settings);
    }

    setSettingValue(settings, path, value) {
        const keys = path.split('.');
        const finalKey = keys.pop();
        const target = keys.reduce((object, key) => object[key] ||= {}, settings);
        target[finalKey] = value;
    }

    syncAttentionSettingsDisabled() {
        const enabled = document.getElementById('panes-attention-indicators')?.checked === true;
        const options = document.getElementById('attention-indicator-options');
        if (options) options.disabled = !enabled;
    }

    getSettingsFromInputs() {
        const settings = { panes: {}, ui: {}, terminal: {}, keybar: {}, diagnostics: {} };
        const hexPattern = /^#[0-9A-Fa-f]{6}$/;

        this.settingsModal.querySelectorAll('[data-setting]').forEach(input => {
            const [category, key] = input.dataset.setting.split('.');
            const value = input.value;

            // Validate hex color format
            if (hexPattern.test(value)) {
                settings[category][key] = value;
            } else {
                // Fall back to default for invalid values
                const defaults = this.getDefaultSettings();
                settings[category][key] = defaults[category]?.[key] || '#000000';
            }
        });

        // Include keybar settings from current state
        settings.keybar = {
            buttons: this.settings.keybar?.buttons || this.getDefaultSettings().keybar.buttons,
            anchor: this.settingsModal.querySelector('input[name="keybar-anchor"]:checked')?.value
                || this.getDefaultSettings().keybar.anchor
        };

        this.settingsModal.querySelectorAll('[data-setting-bool]').forEach(input => {
            this.setSettingValue(settings, input.dataset.settingBool, input.checked);
        });
        this.settingsModal.querySelectorAll('[data-setting-number]').forEach(input => {
            const path = input.dataset.settingNumber;
            const min = Number(input.min || 0);
            const max = Number(input.max || Number.MAX_SAFE_INTEGER);
            const fallback = this.getSettingValue(this.getDefaultSettings(), path) || 0;
            const value = Math.min(max, Math.max(min, Number.parseInt(input.value, 10) || fallback));
            this.setSettingValue(settings, path, value);
        });

        return settings;
    }

    previewSettings() {
        const settings = this.getSettingsFromInputs();
        this.applyUIColors(settings.ui);
    }

    async saveSettings() {
        const settings = this.getSettingsFromInputs();

        try {
            const response = await fetch(this.url('/api/settings'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(settings)
            });

            if (!response.ok) throw new Error('Failed to save settings');

            this.settings = settings;
            this.reconcileAttentionSettings();
            this.renderKeybar();
            this.renderMobileKeybar();
            this.updateKeybarVisibility();
            this.closeModal(this.settingsModal);
            this.toastSuccess('Settings saved');
        } catch (error) {
            console.error('Failed to save settings:', error);
            this.toastError('Failed to save settings');
        }
    }

    async resetSettings() {
        const defaults = this.getDefaultSettings();

        // Update inputs
        for (const [key, value] of Object.entries(defaults.ui)) {
            const colorInput = this.settingsModal.querySelector(`[data-setting="ui.${key}"]`);
            const hexInput = this.settingsModal.querySelector(`[data-setting-hex="ui.${key}"]`);
            if (colorInput) colorInput.value = value;
            if (hexInput) hexInput.value = value;
        }
        for (const [key, value] of Object.entries(defaults.terminal)) {
            const colorInput = this.settingsModal.querySelector(`[data-setting="terminal.${key}"]`);
            const hexInput = this.settingsModal.querySelector(`[data-setting-hex="terminal.${key}"]`);
            if (colorInput) colorInput.value = value;
            if (hexInput) hexInput.value = value;
        }

        // Reset keybar settings
        this.settings.keybar = { ...defaults.keybar };
        this.populateKeybarButtons();
        const keybarAnchorInput = this.settingsModal.querySelector(`input[name="keybar-anchor"][value="${defaults.keybar.anchor}"]`);
        if (keybarAnchorInput) keybarAnchorInput.checked = true;

        this.settingsModal.querySelectorAll('[data-setting-bool]').forEach(input => {
            input.checked = this.getSettingValue(defaults, input.dataset.settingBool) === true;
        });
        this.settingsModal.querySelectorAll('[data-setting-number]').forEach(input => {
            input.value = this.getSettingValue(defaults, input.dataset.settingNumber);
        });
        this.syncAttentionSettingsDisabled();

        // Preview the reset
        this.previewSettings();
    }

    async loadOpenCodeStorageSummary() {
        try {
            const response = await fetch(this.url('/api/storage/opencode'));
            if (!response.ok) throw new Error(await response.text());
            const storage = await response.json();
            this.updateOpenCodeStorageSummary(storage);
            return storage;
        } catch (error) {
            console.error('Failed to load OpenCode storage summary:', error);
            this.updateOpenCodeStorageSummary(null);
            return null;
        }
    }

    updateOpenCodeStorageSummary(storage) {
        if (this.opencodeStorageKeyCount) {
            this.opencodeStorageKeyCount.textContent = storage ? String(storage.keyCount || 0) : '-';
        }
        if (this.opencodeStorageSize) {
            this.opencodeStorageSize.textContent = storage ? this.formatBytes(storage.sizeBytes || 0) : '-';
        }
        if (this.opencodeStorageVersion) {
            this.opencodeStorageVersion.textContent = storage ? String(storage.version || 0) : '-';
        }
    }

    formatBytes(bytes) {
        if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
        const units = ['B', 'KB', 'MB'];
        let value = bytes;
        let unitIndex = 0;
        while (value >= 1024 && unitIndex < units.length - 1) {
            value /= 1024;
            unitIndex++;
        }
        return `${value.toFixed(unitIndex === 0 ? 0 : 1)} ${units[unitIndex]}`;
    }

    toggleStorageActionMenu(event) {
        event.stopPropagation();
        const split = event.currentTarget.closest('.storage-action-split');
        const menu = split?.querySelector('.storage-action-menu');
        const toggle = event.currentTarget;
        if (!menu) return;

        const isOpen = !menu.classList.contains('hidden');
        this.closeStorageActionMenus(split);
        menu.classList.toggle('hidden', isOpen);
        toggle.setAttribute('aria-expanded', String(!isOpen));
        if (!isOpen) menu.querySelector('.storage-action-option')?.focus();
    }

    closeStorageActionMenus(exceptSplit = null) {
        this.settingsModal.querySelectorAll('.storage-action-split').forEach(split => {
            if (split === exceptSplit) return;
            split.querySelector('.storage-action-menu')?.classList.add('hidden');
            split.querySelector('.storage-action-toggle')?.setAttribute('aria-expanded', 'false');
        });
    }

    handleStorageActionMenuClick(event) {
        const option = event.target.closest('.storage-action-option');
        if (!option) return;
        event.stopPropagation();

        const split = option.closest('.storage-action-split');
        this.closeStorageActionMenus();
        switch (option.dataset.storageAction) {
            case 'export-clipboard':
                this.copyOpenCodeStorage();
                break;
            case 'export-file':
                this.downloadOpenCodeStorage();
                break;
            case 'import-clipboard':
                this.pasteOpenCodeStorage();
                break;
            case 'import-file':
                this.opencodeStorageFileInput?.click();
                break;
        }
        split?.querySelector('.storage-action-toggle')?.focus();
    }

    async copyOpenCodeStorage() {
        const storage = await this.loadOpenCodeStorageSummary();
        if (!storage) {
            this.toastError('Failed to copy OpenCode storage');
            return;
        }

        try {
            await navigator.clipboard.writeText(JSON.stringify(storage, null, 2));
            this.toastSuccess('OpenCode storage copied');
        } catch (error) {
            console.error('Failed to copy OpenCode storage:', error);
            this.toastError('Failed to copy OpenCode storage');
        }
    }

    async downloadOpenCodeStorage() {
        const storage = await this.loadOpenCodeStorageSummary();
        if (!storage) {
            this.toastError('Failed to export OpenCode storage');
            return;
        }

        const blob = new Blob([JSON.stringify(storage, null, 2)], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const link = document.createElement('a');
        link.href = url;
        link.download = 'webmux-opencode-storage.json';
        document.body.appendChild(link);
        link.click();
        link.remove();
        URL.revokeObjectURL(url);
        this.toastSuccess('OpenCode storage downloaded');
    }

    async importOpenCodeStorage() {
        const file = this.opencodeStorageFileInput?.files?.[0];
        if (!file) return;

        try {
            const text = await file.text();
            await this.replaceOpenCodeStorageFromText(text);
            this.toastSuccess('OpenCode storage imported');
        } catch (error) {
            console.error('Failed to import OpenCode storage:', error);
            this.toastError(`Failed to import OpenCode storage: ${error.message}`);
        } finally {
            if (this.opencodeStorageFileInput) this.opencodeStorageFileInput.value = '';
        }
    }

    async pasteOpenCodeStorage() {
        try {
            const text = await navigator.clipboard.readText();
            if (!text.trim()) {
                this.toastWarning('Clipboard is empty');
                return;
            }
            await this.replaceOpenCodeStorageFromText(text);
            this.toastSuccess('OpenCode storage pasted');
        } catch (error) {
            console.error('Failed to paste OpenCode storage:', error);
            this.toastError(`Failed to paste OpenCode storage: ${error.message}`);
        }
    }

    async replaceOpenCodeStorageFromText(text) {
        const items = this.parseOpenCodeStorageImport(text);
        return this.replaceOpenCodeStorageItems(items);
    }

    async replaceOpenCodeStorageItems(items) {
        const response = await fetch(this.url('/api/storage/opencode'), {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ items })
        });
        if (!response.ok) throw new Error(await response.text());

        const storage = await response.json();
        this.updateOpenCodeStorageSummary(storage);
        return storage;
    }

    parseOpenCodeStorageImport(text) {
        let parsed;
        try {
            parsed = JSON.parse(text);
        } catch {
            throw new Error('Invalid JSON');
        }
        if (!this.isPlainObject(parsed)) {
            throw new Error('Expected a JSON object');
        }

        if (Object.prototype.hasOwnProperty.call(parsed, 'items')) {
            const allowed = new Set(['namespace', 'items', 'version', 'updatedBy', 'keyCount', 'sizeBytes']);
            for (const key of Object.keys(parsed)) {
                if (!allowed.has(key)) throw new Error(`Unexpected export field: ${key}`);
            }
            if (typeof parsed.namespace !== 'undefined' && parsed.namespace !== 'opencode') {
                throw new Error('Storage export is not for the opencode namespace');
            }
            if (typeof parsed.version !== 'undefined' && (!Number.isInteger(parsed.version) || parsed.version < 0)) {
                throw new Error('Storage version must be a non-negative integer');
            }
            if (typeof parsed.keyCount !== 'undefined' && (!Number.isInteger(parsed.keyCount) || parsed.keyCount < 0)) {
                throw new Error('Storage keyCount must be a non-negative integer');
            }
            if (typeof parsed.sizeBytes !== 'undefined' && (!Number.isInteger(parsed.sizeBytes) || parsed.sizeBytes < 0)) {
                throw new Error('Storage sizeBytes must be a non-negative integer');
            }
            return this.validateOpenCodeStorageItems(parsed.items);
        }

        return this.validateOpenCodeStorageItems(parsed);
    }

    validateOpenCodeStorageItems(items) {
        if (!this.isPlainObject(items)) {
            throw new Error('Storage items must be an object');
        }

        const result = {};
        for (const [key, value] of Object.entries(items)) {
            if (!key) throw new Error('Storage keys cannot be empty');
            if (typeof value !== 'string') throw new Error(`Storage value for ${key} must be a string`);
            result[key] = value;
        }
        return result;
    }

    buildOpenCodePreferenceStorage(items) {
        const result = {};
        const preservedKeys = new Set([
            'settings.v3',
            'highlights.v1',
            'opencode-color-scheme',
            'opencode-theme-id',
            'opencode-theme-css-light',
            'opencode-theme-css-dark',
            'opencode.global.dat:model',
            'opencode.global.dat:command.catalog.v1'
        ]);

        for (const [key, value] of Object.entries(items)) {
            if (key.startsWith('opencode.workspace.')) continue;
            if (key === 'opencode.global.dat:notification' || key === 'opencode.global.dat:prompt-history') continue;
            if (key === 'opencode.global.dat:layout') {
                result[key] = this.cleanOpenCodeLayout(value);
                continue;
            }
            if (key === 'opencode.global.dat:layout.page') {
                result[key] = this.cleanOpenCodeLayoutPage(value);
                continue;
            }
            if (key === 'opencode.global.dat:server') {
                result[key] = this.cleanOpenCodeServer(value);
                continue;
            }
            if (preservedKeys.has(key) || key.startsWith('opencode-theme-')) {
                result[key] = value;
            }
        }

        return result;
    }

    cleanOpenCodeLayout(value) {
        try {
            const layout = JSON.parse(value);
            layout.sessionTabs = {};
            layout.sessionView = {};
            layout.handoff = {};
            return JSON.stringify(layout);
        } catch {
            return '{}';
        }
    }

    cleanOpenCodeLayoutPage(value) {
        try {
            const page = JSON.parse(value);
            page.lastProjectSession = {};
            page.workspaceOrder = {};
            page.workspaceName = {};
            page.workspaceBranchName = {};
            page.workspaceExpanded = {};
            return JSON.stringify(page);
        } catch {
            return JSON.stringify({ lastProjectSession: {} });
        }
    }

    cleanOpenCodeServer(value) {
        try {
            const server = JSON.parse(value);
            server.projects = {};
            server.lastProject = {};
            return JSON.stringify(server);
        } catch {
            return JSON.stringify({ list: [], projects: {}, lastProject: {} });
        }
    }

    async resetOpenCodeSessionState() {
        if (!confirm('Reset OpenCode session state? This removes workspace/session caches, project lists, notifications, prompt history, and last-session pointers while keeping theme, settings, model, and server preferences.')) {
            return;
        }

        try {
            const storage = await this.loadOpenCodeStorageSummary();
            if (!storage?.items) throw new Error('Failed to load OpenCode storage');
            const items = this.buildOpenCodePreferenceStorage(storage.items);
            const updated = await this.replaceOpenCodeStorageItems(items);
            this.updateOpenCodeStorageSummary(updated);
            this.toastSuccess('OpenCode session state reset. Reload OpenCode panes to start from the cleaned state.');
        } catch (error) {
            console.error('Failed to reset OpenCode session state:', error);
            this.toastError(`Failed to reset OpenCode session state: ${error.message}`);
        }
    }

    isPlainObject(value) {
        return value !== null && typeof value === 'object' && !Array.isArray(value);
    }

    async clearOpenCodeStorage() {
        if (!confirm('Clear OpenCode web storage? This resets OpenCode web UI settings and model UI state for webmux.')) {
            return;
        }

        try {
            const response = await fetch(this.url('/api/storage/opencode'), { method: 'DELETE' });
            if (!response.ok) throw new Error(await response.text());
            const storage = await response.json();
            this.updateOpenCodeStorageSummary(storage);
            this.toastSuccess('OpenCode storage cleared');
        } catch (error) {
            console.error('Failed to clear OpenCode storage:', error);
            this.toastError('Failed to clear OpenCode storage');
        }
    }

    // Keybar Settings
    // ===============

    populateKeybarButtons() {
        const buttonsList = document.getElementById('keybar-buttons-list');
        if (!buttonsList) return;

        buttonsList.innerHTML = '';

        const buttons = this.getKeybarButtonsFromSettings();

        buttons.forEach((keys, index) => {
            const buttonItem = this.createKeybarButtonItem(keys, index);
            buttonsList.appendChild(buttonItem);
        });
    }

    createKeybarButtonItem(keys, index) {
        const div = document.createElement('div');
        div.className = 'keybar-button-item';
        div.draggable = true;
        div.dataset.index = index;

        const isValid = this.validateKeyCombo(keys);
        if (!isValid) {
            div.classList.add('invalid');
        }

        const label = this.formatKeyLabel(keys);
        const title = this.formatKeyTitle(keys);

        const buttons = this.getKeybarButtonsFromSettings();
        const isFirst = index === 0;
        const isLast = index === buttons.length - 1;

        div.innerHTML = `
            <span class="keybar-button-drag" title="Drag to reorder">
                <svg viewBox="0 0 24 24" width="16" height="16"><path fill="currentColor" d="M11 18c0 1.1-.9 2-2 2s-2-.9-2-2 .9-2 2-2 2 .9 2 2zm-2-8c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm0-6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm6 4c1.1 0 2-.9 2-2s-.9-2-2-2-2 .9-2 2 .9 2 2 2zm0 2c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2zm0 6c-1.1 0-2 .9-2 2s.9 2 2 2 2-.9 2-2-.9-2-2-2z"/></svg>
            </span>
            <div class="keybar-button-arrows">
                <button class="keybar-button-up" title="Move up" ${isFirst ? 'disabled' : ''}>
                    <svg viewBox="0 0 24 24" width="12" height="12"><path fill="currentColor" d="M7.41 15.41L12 10.83l4.59 4.58L18 14l-6-6-6 6z"/></svg>
                </button>
                <button class="keybar-button-down" title="Move down" ${isLast ? 'disabled' : ''}>
                    <svg viewBox="0 0 24 24" width="12" height="12"><path fill="currentColor" d="M7.41 8.59L12 13.17l4.59-4.58L18 10l-6 6-6-6z"/></svg>
                </button>
            </div>
            <span class="keybar-button-keys">${keys}</span>
            <span class="keybar-button-label">${label}</span>
            ${!isValid ? '<span class="keybar-button-invalid">Invalid</span>' : ''}
            <button class="keybar-button-remove" data-index="${index}" title="Remove">
                <svg viewBox="0 0 24 24" width="14" height="14"><path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/></svg>
            </button>
        `;

        // Add button handlers
        const removeBtn = div.querySelector('.keybar-button-remove');
        removeBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            this.removeKeybarButton(index);
        });

        const upBtn = div.querySelector('.keybar-button-up');
        upBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            if (index > 0) {
                this.reorderKeybarButton(index, index - 1);
            }
        });

        const downBtn = div.querySelector('.keybar-button-down');
        downBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            if (index < buttons.length - 1) {
                this.reorderKeybarButton(index, index + 1);
            }
        });

        // Drag handlers for reordering
        div.addEventListener('dragstart', (e) => {
            div.classList.add('dragging');
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', index.toString());
        });

        div.addEventListener('dragend', () => {
            div.classList.remove('dragging');
        });

        div.addEventListener('dragover', (e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            const dragging = document.querySelector('.keybar-button-item.dragging');
            if (dragging && dragging !== div) {
                div.classList.add('drag-over');
            }
        });

        div.addEventListener('dragleave', () => {
            div.classList.remove('drag-over');
        });

        div.addEventListener('drop', (e) => {
            e.preventDefault();
            div.classList.remove('drag-over');
            const fromIndex = parseInt(e.dataTransfer.getData('text/plain'));
            const toIndex = index;
            if (fromIndex !== toIndex) {
                this.reorderKeybarButton(fromIndex, toIndex);
            }
        });

        return div;
    }

    reorderKeybarButton(fromIndex, toIndex) {
        const buttons = [...this.getKeybarButtonsFromSettings()];
        const [moved] = buttons.splice(fromIndex, 1);
        buttons.splice(toIndex, 0, moved);
        this.settings.keybar = { ...this.settings.keybar, buttons };
        this.populateKeybarButtons();
    }

    removeKeybarButton(index) {
        if (!this.settings.keybar) {
            this.settings.keybar = { ...this.getDefaultSettings().keybar, buttons: [...this.getDefaultSettings().keybar.buttons] };
        }

        this.settings.keybar.buttons.splice(index, 1);
        this.populateKeybarButtons();
    }

    addKeybarButton() {
        const keysInput = document.getElementById('new-keybar-keys');
        const rawKeys = keysInput.value.trim();

        if (!rawKeys) {
            this.showKeybarInputError('Enter a key combination');
            return;
        }

        if (!this.validateKeyCombo(rawKeys)) {
            this.showKeybarInputError('Invalid key combination');
            return;
        }

        // Normalize the key combo
        const keys = this.normalizeKeyCombo(rawKeys);

        if (!this.settings.keybar) {
            this.settings.keybar = { ...this.getDefaultSettings().keybar, buttons: [...this.getDefaultSettings().keybar.buttons] };
        }

        // Check for duplicates (compare normalized)
        const existingNormalized = this.settings.keybar.buttons.map(k => this.normalizeKeyCombo(k));
        if (existingNormalized.includes(keys)) {
            this.showKeybarInputError('Key combination already exists');
            return;
        }

        this.hideKeybarInputError();
        this.settings.keybar.buttons.push(keys);
        this.populateKeybarButtons();

        // Clear input
        keysInput.value = '';
    }

    getKeybarButtonsFromSettings() {
        const buttons = this.settings.keybar?.buttons || this.getDefaultSettings().keybar.buttons;
        // Handle legacy format (array of objects)
        if (buttons.length > 0 && typeof buttons[0] === 'object') {
            return buttons.map(b => b.keys);
        }
        return buttons;
    }

    renderKeybar() {
        if (!this.keybar) return;

        const buttons = this.getKeybarButtonsFromSettings();

        // Clear existing buttons
        this.keybar.innerHTML = '';

        // Only render valid buttons
        buttons.filter(keys => this.validateKeyCombo(keys)).forEach(keys => {
            const btn = document.createElement('button');
            btn.className = 'keybar-btn';
            btn.dataset.keys = keys;
            btn.title = this.formatKeyTitle(keys);
            btn.setAttribute('aria-label', this.formatKeyLabel(keys));

            const label = document.createElement('span');
            label.className = 'key-label';
            label.textContent = this.formatKeyLabel(keys);

            btn.appendChild(label);
            this.keybar.appendChild(btn);
        });

        // Re-bind event listeners for new buttons
        this.bindKeybarEvents();
    }

    renderMobileKeybar() {
        if (!this.mobileBottomToolbar) return;

        const mobileKeybarScroll = this.mobileBottomToolbar.querySelector('.mobile-keybar-scroll');
        if (!mobileKeybarScroll) return;

        const buttons = this.getKeybarButtonsFromSettings();

        // Clear existing buttons
        const existingBtns = mobileKeybarScroll.querySelectorAll('.mobile-keybar-btn');
        existingBtns.forEach(btn => btn.remove());

        // Add every configured button; the mobile keybar scrolls horizontally.
        const validButtons = buttons.filter(keys => this.validateKeyCombo(keys));
        validButtons.forEach(keys => {
            const btn = document.createElement('button');
            btn.className = 'mobile-keybar-btn';
            btn.dataset.keys = keys;
            btn.title = this.formatKeyTitle(keys);
            btn.setAttribute('aria-label', this.formatKeyLabel(keys));

            const label = document.createElement('span');
            label.className = 'mobile-key-label';
            label.textContent = this.formatKeyLabel(keys);

            btn.appendChild(label);
            mobileKeybarScroll.appendChild(btn);
        });

        // Re-bind event listeners for new buttons
        this.bindMobileKeybarEvents();
    }

    bindKeybarEvents() {
        if (!this.keybar) return;

        // Keybar button clicks - send input to active pane
        this.keybar.querySelectorAll('.keybar-btn').forEach(btn => {
            btn.addEventListener('pointerdown', (e) => {
                e.preventDefault();
            });
            btn.addEventListener('mousedown', (e) => {
                e.preventDefault(); // Prevent focus change
            });
            btn.addEventListener('touchstart', (e) => {
                e.preventDefault();
            }, { passive: false });
            btn.addEventListener('click', (e) => {
                e.preventDefault();
                btn.blur(); // Remove focus so arrow keys don't navigate buttons
                const keys = btn.dataset.keys;
                if (keys) {
                    this.handleKeybarAction(keys);
                }
            });
        });
    }

    // Handle keybar button action - either send input or perform special action
    async handleKeybarAction(keys) {
        // Handle special actions
        if (keys === 'Paste') {
            await this.pasteFromClipboard();
            return;
        }

        // Regular key combo - send to active pane
        await this.sendInputToActivePane({ keys: [keys] });
        this.refocusKeybarTargetPane();
    }

    refocusKeybarTargetPane() {
        if (this.focusedPaneId && this.paneSupportsKeybarInput(this.panes.get(this.focusedPaneId))) {
            this.focusPane(this.focusedPaneId);
        }
    }

    // Paste server-side clipboard content to active pane
    async pasteFromClipboard() {
        try {
            const resp = await fetch(this.url('/api/clipboard/request?type=text/plain'));
            if (!resp.ok) {
                this.toastError('Failed to read clipboard');
                return;
            }
            const text = await resp.text();
            if (!text) {
                this.toastWarning('Clipboard is empty');
                return;
            }
            await this.sendInputToActivePane({
                sequence: [{ type: 'text', value: text }]
            });
            this.refocusKeybarTargetPane();
        } catch (err) {
            console.error('[clipboard] Failed to paste:', err);
            this.toastError('Failed to read clipboard');
        }
    }

    bindMobileKeybarEvents() {
        if (!this.mobileBottomToolbar) return;

        const scroll = this.mobileBottomToolbar.querySelector('.mobile-keybar-scroll');
        if (!scroll || scroll.dataset.mobileKeybarBound === 'true') return;

        scroll.dataset.mobileKeybarBound = 'true';
        let startX = 0;
        let startY = 0;
        let didDrag = false;
        let activePointerId = null;
        let activeButton = null;

        scroll.addEventListener('pointerdown', (e) => {
            if (e.button !== 0 && e.pointerType === 'mouse') return;

            activePointerId = e.pointerId;
            activeButton = e.target.closest('.mobile-keybar-btn');
            startX = e.clientX;
            startY = e.clientY;
            didDrag = false;
        });

        scroll.addEventListener('pointermove', (e) => {
            if (e.pointerId !== activePointerId) return;

            const dx = e.clientX - startX;
            const dy = e.clientY - startY;
            if (Math.abs(dx) > 4 || Math.abs(dy) > 4) {
                didDrag = true;
            }
        });

        const resetPointer = (e) => {
            if (e.pointerId !== activePointerId) return;

            activePointerId = null;
            activeButton = null;
        };

        const finishPointer = (e) => {
            if (e.pointerId !== activePointerId) return;

            if (!didDrag && activeButton?.dataset.keys) {
                e.preventDefault();
                activeButton.blur();
                this.handleKeybarAction(activeButton.dataset.keys);
            }

            resetPointer(e);
        };

        scroll.addEventListener('pointerup', finishPointer);
        scroll.addEventListener('pointercancel', resetPointer);
    }

    bindMobileArrowPadEvents() {
        if (!this.mobileArrowPad || this.mobileArrowPad.dataset.bound === 'true') return;

        this.mobileArrowPad.dataset.bound = 'true';
        const initialThreshold = 22;
        const repeatThreshold = 36;
        const dominance = 1.25;
        let activePointerId = null;
        let startX = 0;
        let startY = 0;
        let suppressClick = false;
        let hasSent = false;

        const reset = (e) => {
            if (e.pointerId !== activePointerId) return;

            this.mobileArrowPad.classList.remove('active');
            if (this.mobileArrowPad.hasPointerCapture?.(e.pointerId)) {
                this.mobileArrowPad.releasePointerCapture(e.pointerId);
            }
            activePointerId = null;
        };

        this.mobileArrowPad.addEventListener('pointerdown', (e) => {
            if (e.button !== 0 && e.pointerType === 'mouse') return;

            e.preventDefault();
            activePointerId = e.pointerId;
            startX = e.clientX;
            startY = e.clientY;
            suppressClick = false;
            hasSent = false;
            this.mobileArrowPad.classList.add('active');
            this.mobileArrowPad.setPointerCapture?.(e.pointerId);
        });

        this.mobileArrowPad.addEventListener('pointermove', (e) => {
            if (e.pointerId !== activePointerId) return;

            const dx = e.clientX - startX;
            const dy = e.clientY - startY;
            const absX = Math.abs(dx);
            const absY = Math.abs(dy);
            const threshold = hasSent ? repeatThreshold : initialThreshold;
            let key = null;

            if (absX >= threshold && absX >= absY * dominance) {
                key = dx > 0 ? 'Right' : 'Left';
            } else if (absY >= threshold && absY >= absX * dominance) {
                key = dy > 0 ? 'Down' : 'Up';
            }

            if (!key) return;

            suppressClick = true;
            hasSent = true;
            e.preventDefault();
            this.handleKeybarAction(key);
            startX = e.clientX;
            startY = e.clientY;
        });

        this.mobileArrowPad.addEventListener('pointerup', reset);
        this.mobileArrowPad.addEventListener('pointercancel', reset);
        this.mobileArrowPad.addEventListener('click', (e) => {
            if (suppressClick) {
                e.preventDefault();
                suppressClick = false;
            }
        });
    }

    async exportSettings() {
        const settings = this.getSettingsFromInputs();
        const activeTab = this.settingsModal.querySelector('.settings-tab.active').dataset.tab;

        let yaml;
        if (activeTab === 'terminal') {
            yaml = this.terminalToBase24Yaml(settings.terminal);
        } else {
            yaml = this.uiToYaml(settings.ui);
        }

        try {
            await navigator.clipboard.writeText(yaml);
            // Visual feedback only - no toast for UI actions
            const originalText = this.settingsExportBtn.textContent;
            this.settingsExportBtn.textContent = 'Copied!';
            this.settingsExportBtn.classList.add('success');
            setTimeout(() => {
                this.settingsExportBtn.textContent = originalText;
                this.settingsExportBtn.classList.remove('success');
            }, 1500);
        } catch (error) {
            console.error('Failed to copy to clipboard:', error);
            this.toastError('Failed to copy to clipboard');
        }
    }

    terminalToBase24Yaml(terminal) {
        const lines = [
            'scheme: "Exported Theme"',
            'author: "Terminal Multiplexer"'
        ];

        // Base24 keys in order
        const keys = [
            'base00', 'base01', 'base02', 'base03', 'base04', 'base05', 'base06', 'base07',
            'base08', 'base09', 'base0A', 'base0B', 'base0C', 'base0D', 'base0E', 'base0F',
            'base10', 'base11', 'base12', 'base13', 'base14', 'base15', 'base16', 'base17'
        ];

        for (const key of keys) {
            const value = terminal[key] || '#000000';
            // Remove # prefix for Base24 format
            lines.push(`${key}: "${value.replace('#', '')}"`);
        }

        return lines.join('\n');
    }

    uiToYaml(ui) {
        const lines = [
            'scheme: "UI Theme"',
            'author: "Terminal Multiplexer"'
        ];

        for (const [key, value] of Object.entries(ui)) {
            lines.push(`${key}: "${value.replace('#', '')}"`);
        }

        return lines.join('\n');
    }

    async importSettings() {
        try {
            const text = await navigator.clipboard.readText();
            if (!text.trim()) {
                this.toastWarning('Clipboard is empty');
                return;
            }

            const parsed = this.parseYaml(text);
            const activeTab = this.settingsModal.querySelector('.settings-tab.active').dataset.tab;

            // Validate before applying
            if (activeTab === 'terminal') {
                if (!this.validateBase24Theme(parsed)) {
                    this.toastError('Invalid Base24 theme format. Expected base00-base17 color values.');
                    return;
                }
                this.importBase24Theme(parsed);
            } else {
                if (!this.validateUITheme(parsed)) {
                    this.toastError('Invalid UI theme format. Expected bgPrimary, textPrimary, etc.');
                    return;
                }
                this.importUITheme(parsed);
            }

            this.previewSettings();

            // Visual feedback only - no toast for UI actions
            const originalText = this.settingsImportBtn.textContent;
            this.settingsImportBtn.textContent = 'Imported!';
            this.settingsImportBtn.classList.add('success');
            setTimeout(() => {
                this.settingsImportBtn.textContent = originalText;
                this.settingsImportBtn.classList.remove('success');
            }, 1500);
        } catch (error) {
            console.error('Failed to read clipboard:', error);
            this.toastError('Failed to read clipboard. Make sure you have granted clipboard permissions.');
        }
    }

    validateBase24Theme(parsed) {
        // Check for at least some Base24 keys
        const requiredKeys = ['base00', 'base05', 'base08'];
        return requiredKeys.some(key => parsed[key] && /^[0-9A-Fa-f]{6}$/.test(parsed[key]));
    }

    validateUITheme(parsed) {
        // Check for at least some UI keys
        const requiredKeys = ['bgPrimary', 'textPrimary'];
        return requiredKeys.some(key => parsed[key] && /^[0-9A-Fa-f]{6}$/.test(parsed[key]));
    }

    parseYaml(text) {
        // Simple YAML parser for key: "value" format
        const result = {};
        const lines = text.split('\n');

        for (const line of lines) {
            const match = line.match(/^(\w+):\s*"?([^"]*)"?\s*$/);
            if (match) {
                result[match[1]] = match[2];
            }
        }

        return result;
    }

    importBase24Theme(parsed) {
        const keys = [
            'base00', 'base01', 'base02', 'base03', 'base04', 'base05', 'base06', 'base07',
            'base08', 'base09', 'base0A', 'base0B', 'base0C', 'base0D', 'base0E', 'base0F',
            'base10', 'base11', 'base12', 'base13', 'base14', 'base15', 'base16', 'base17'
        ];

        for (const key of keys) {
            if (parsed[key]) {
                let value = parsed[key];
                if (!value.startsWith('#')) value = '#' + value;

                const colorInput = this.settingsModal.querySelector(`[data-setting="terminal.${key}"]`);
                const hexInput = this.settingsModal.querySelector(`[data-setting-hex="terminal.${key}"]`);
                if (colorInput) colorInput.value = value;
                if (hexInput) hexInput.value = value;
            }
        }
    }

    importUITheme(parsed) {
        const uiKeys = ['bgPrimary', 'bgSecondary', 'bgTertiary', 'textPrimary', 'textSecondary', 'textMuted', 'accent', 'accentHover', 'border'];

        for (const key of uiKeys) {
            if (parsed[key]) {
                let value = parsed[key];
                if (!value.startsWith('#')) value = '#' + value;

                const colorInput = this.settingsModal.querySelector(`[data-setting="ui.${key}"]`);
                const hexInput = this.settingsModal.querySelector(`[data-setting-hex="ui.${key}"]`);
                if (colorInput) colorInput.value = value;
                if (hexInput) hexInput.value = value;
            }
        }
    }

    // Toast Notifications
    // ===================

    toast(message, type = 'info', duration = 4000) {
        const container = document.getElementById('toast-container');
        if (!container) return;

        const toast = document.createElement('div');
        toast.className = `toast toast-${type}`;

        const icons = {
            error: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/></svg>',
            success: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>',
            warning: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>',
            info: '<svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/></svg>'
        };

        toast.innerHTML = `
            <span class="toast-icon">${icons[type] || icons.info}</span>
            <span class="toast-message">${message}</span>
            <button class="toast-close" title="Dismiss" aria-label="Dismiss notification">
                <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/></svg>
            </button>
        `;

        const closeBtn = toast.querySelector('.toast-close');
        const dismiss = () => {
            toast.classList.add('toast-out');
            setTimeout(() => toast.remove(), 200);
        };

        closeBtn.addEventListener('click', dismiss);

        container.appendChild(toast);

        if (duration > 0) {
            setTimeout(dismiss, duration);
        }

        return toast;
    }

    toastError(message, duration = 5000) {
        return this.toast(message, 'error', duration);
    }

    toastSuccess(message, duration = 3000) {
        return this.toast(message, 'success', duration);
    }

    toastWarning(message, duration = 4000) {
        return this.toast(message, 'warning', duration);
    }

    toastInfo(message, duration = 4000) {
        return this.toast(message, 'info', duration);
    }

    // Alias for convenience
    showToast(message, type = 'info', duration = 4000) {
        return this.toast(message, type, duration);
    }

    // Scratch Pad
    // ===========

    showScratchPad(text = '') {
        const container = document.getElementById('toast-container');
        if (!container) return null;

        // If scratch pad exists, just update it
        let pad = container.querySelector('.scratch-pad');
        if (pad) {
            const textarea = pad.querySelector('.scratch-pad-content');
            if (textarea && text) {
                textarea.value = text;
            }
            return pad;
        }

        // Create new scratch pad
        pad = document.createElement('div');
        pad.className = 'scratch-pad';
        pad.setAttribute('role', 'complementary');
        pad.setAttribute('aria-label', 'Scratch pad');
        pad.innerHTML = `
            <div class="scratch-pad-header">
                <button class="scratch-pad-icon-btn" title="Copy to clipboard" aria-label="Copy scratch pad content">
                    <svg class="icon-default" viewBox="0 0 24 24" aria-hidden="true">
                        <path fill="currentColor" d="M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V5h14v14zm-2-2H7v-2h10v2zm0-4H7v-2h10v2zm0-4H7V7h10v2z"/>
                    </svg>
                    <svg class="icon-copy" viewBox="0 0 24 24" aria-hidden="true">
                        <path fill="currentColor" d="M16 1H4c-1.1 0-2 .9-2 2v14h2V3h12V1zm3 4H8c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h11c1.1 0 2-.9 2-2V7c0-1.1-.9-2-2-2zm0 16H8V7h11v14z"/>
                    </svg>
                </button>
                <span class="scratch-pad-title">Scratch Pad</span>
                <button class="scratch-pad-close" title="Close" aria-label="Close scratch pad">
                    <svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/></svg>
                </button>
            </div>
            <textarea class="scratch-pad-content" placeholder="Paste or type text here..." aria-label="Scratch pad content">${this.escapeHtml(text)}</textarea>
        `;

        const closeBtn = pad.querySelector('.scratch-pad-close');
        closeBtn.addEventListener('click', () => this.hideScratchPad());

        const iconBtn = pad.querySelector('.scratch-pad-icon-btn');
        const textarea = pad.querySelector('.scratch-pad-content');

        // Copy button (icon transforms on hover)
        iconBtn.addEventListener('click', async () => {
            try {
                await navigator.clipboard.writeText(textarea.value);
                iconBtn.classList.add('copied');
                setTimeout(() => iconBtn.classList.remove('copied'), 1500);
            } catch (e) {
                this.toastError('Failed to copy');
            }
        });

        // Sync to server on changes (debounced)
        let syncTimeout = null;
        textarea.addEventListener('input', () => {
            clearTimeout(syncTimeout);
            syncTimeout = setTimeout(() => {
                this.syncScratchToServer(textarea.value);
            }, 500);
        });

        // Insert at the top
        container.insertBefore(pad, container.firstChild);

        this.updateScratchButtonState();
        return pad;
    }

    hideScratchPad() {
        const container = document.getElementById('toast-container');
        const pad = container?.querySelector('.scratch-pad');
        if (pad) {
            pad.classList.add('scratch-out');
            setTimeout(() => {
                pad.remove();
                this.updateScratchButtonState();
            }, 200);
        } else {
            this.updateScratchButtonState();
        }
    }

    getScratchPadText() {
        const container = document.getElementById('toast-container');
        const textarea = container?.querySelector('.scratch-pad-content');
        return textarea?.value || '';
    }

    setScratchPadText(text) {
        const pad = this.showScratchPad(text);
        return pad;
    }

    async toggleScratchPad(text = null) {
        const container = document.getElementById('toast-container');
        const pad = container?.querySelector('.scratch-pad');

        if (pad) {
            // Already visible - hide it
            this.hideScratchPad();
        } else {
            // Not visible - fetch current content if no text provided
            if (text === null) {
                try {
                    const response = await fetch(this.url('/api/scratch'));
                    if (response.ok) {
                        const data = await response.json();
                        text = data.text || '';
                    } else {
                        text = '';
                    }
                } catch (e) {
                    text = '';
                }
            }
            this.showScratchPad(text);
        }
    }

    isScratchPadVisible() {
        const container = document.getElementById('toast-container');
        return !!container?.querySelector('.scratch-pad');
    }

    updateScratchButtonState() {
        if (this.toggleScratchBtn) {
            this.toggleScratchBtn.classList.toggle('active', this.isScratchPadVisible());
        }
    }

    connectScratchEvents() {
        if (this.connectionMode !== 'active') return;
        this.stopScratchEvents();
        // Connect to SSE for scratch pad updates from CLI.
        const retryDelay = 2000;

        const connect = () => {
            if (this.connectionMode !== 'active') return;
            const es = new EventSource(this.url('/api/scratch/events'));
            this.scratchEventSource = es;

            es.onmessage = (e) => {
                try {
                    const data = JSON.parse(e.data);
                    const currentText = this.getScratchPadText();

                    switch (data.type) {
                        case 'init':
                            // Initial connection - don't show unless there's content
                            // (user can toggle it open manually)
                            break;

                        case 'toggle':
                            // Toggle visibility
                            this.toggleScratchPad(data.text);
                            break;

                        case 'clear':
                            // Clear and close
                            this.hideScratchPad();
                            break;

                        case 'text':
                        default:
                            // Update text - only if different and not our own edit
                            if (data.text !== currentText && data.text !== this._lastSyncedText) {
                                if (data.text) {
                                    this.showScratchPad(data.text);
                                } else {
                                    this.hideScratchPad();
                                }
                            }
                            break;
                    }
                } catch (err) {
                    console.error('Failed to parse scratch event:', err);
                }
            };

            es.onerror = () => {
                es.close();
                if (this.scratchEventSource === es) this.scratchEventSource = null;
                if (this.connectionMode === 'active' && !this.scratchReconnectTimer) {
                    this.scratchReconnectTimer = setTimeout(() => {
                        this.scratchReconnectTimer = null;
                        connect();
                    }, retryDelay);
                }
            };
        };

        connect();
    }

    stopScratchEvents() {
        if (this.scratchReconnectTimer) {
            clearTimeout(this.scratchReconnectTimer);
            this.scratchReconnectTimer = null;
        }
        const source = this.scratchEventSource;
        this.scratchEventSource = null;
        source?.close();
    }

    // Sync scratch pad text to server when user edits
    async syncScratchToServer(text) {
        this._lastSyncedText = text;
        try {
            await fetch(this.url('/api/scratch'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ text })
            });
        } catch (err) {
            console.error('Failed to sync scratch pad:', err);
        }
    }

    // Marked Files
    // ============

    connectMarkedEvents() {
        if (this.connectionMode !== 'active') return;
        this.stopMarkedEvents();
        // Connect to SSE for marked files updates.
        const retryDelay = 2000;

        const connect = () => {
            if (this.connectionMode !== 'active') return;
            const es = new EventSource(this.url('/api/marked/events'));
            this.markedEventSource = es;

            es.onmessage = (e) => {
                try {
                    const data = JSON.parse(e.data);
                    this.markedFiles = data.files || [];
                    this.updateMarkedUI();
                } catch (err) {
                    console.error('Failed to parse marked event:', err);
                }
            };

            es.onerror = () => {
                es.close();
                if (this.markedEventSource === es) this.markedEventSource = null;
                if (this.connectionMode === 'active' && !this.markedReconnectTimer) {
                    this.markedReconnectTimer = setTimeout(() => {
                        this.markedReconnectTimer = null;
                        connect();
                    }, retryDelay);
                }
            };
        };

        connect();
    }

    stopMarkedEvents() {
        if (this.markedReconnectTimer) {
            clearTimeout(this.markedReconnectTimer);
            this.markedReconnectTimer = null;
        }
        const source = this.markedEventSource;
        this.markedEventSource = null;
        source?.close();
    }

    async markFile(path) {
        try {
            const response = await fetch(this.url('/api/marked'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path })
            });
            if (!response.ok) {
                const err = await response.text();
                this.showToast(err, 'error');
            }
        } catch (err) {
            console.error('Failed to mark file:', err);
            this.showToast('Failed to mark file', 'error');
        }
    }

    async unmarkFile(path) {
        try {
            await fetch(this.url(`/api/marked?path=${encodeURIComponent(path)}`), {
                method: 'DELETE'
            });
        } catch (err) {
            console.error('Failed to unmark file:', err);
        }
    }

    async clearMarkedFiles() {
        try {
            await fetch(this.url('/api/marked'), { method: 'DELETE' });
        } catch (err) {
            console.error('Failed to clear marked files:', err);
        }
    }

    async downloadMarkedFiles() {
        if (this.markedFiles.length === 0) return;

        // Trigger download
        window.open(this.url('/api/marked/download'), '_blank');
    }

    async downloadSingleMarked(path) {
        // Download single item from marked list (handles both files and directories)
        // The endpoint will unmark after download
        window.open(this.url(`/api/marked/download?path=${encodeURIComponent(path)}`), '_blank');
    }

    updateMarkedUI() {
        // Update sidekick panel visibility - only show when download modal is open AND files are marked
        const downloadModalOpen = !this.downloadModal.classList.contains('hidden');
        const modalContent = this.downloadModal.querySelector('.modal-content');
        if (this.markedFiles.length > 0 && downloadModalOpen) {
            this.markedSidekick.classList.remove('hidden');
            modalContent?.classList.add('has-sidekick');
            // Constrain sidekick height to be smaller than the modal (not needed on mobile)
            if (!this.mobileMode) {
                this.constrainSidekickHeight();
            }
        } else {
            this.markedSidekick.classList.add('hidden');
            modalContent?.classList.remove('has-sidekick');
        }

        // Update mobile marked files UI
        this.updateMobileMarkedUI();

        // Update sidekick list
        this.renderMarkedList();

        // Update marked toast
        this.updateMarkedToast();

        // Update mark buttons in file browser if visible
        this.updateMarkButtons();
    }

    constrainSidekickHeight() {
        // Get the modal content's actual height and constrain sidekick to be smaller
        const modalContent = this.downloadModal.querySelector('.modal-content');
        if (modalContent) {
            const modalHeight = modalContent.offsetHeight;
            // Sidekick should be 40px shorter than modal (20px margin top & bottom)
            this.markedSidekick.style.maxHeight = `${modalHeight - 40}px`;
        }
    }

    renderMarkedList() {
        this.markedList.innerHTML = this.markedFiles.map(file => {
            const icon = file.isDir
                ? '<path fill="currentColor" d="M10 4H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2h-8l-2-2z"/>'
                : '<path fill="currentColor" d="M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z"/>';
            return `
            <div class="marked-item ${file.isDir ? 'directory' : ''}" data-path="${this.escapeHtml(file.path)}">
                <svg class="icon" viewBox="0 0 24 24" width="16" height="16">
                    ${icon}
                </svg>
                <span class="name" title="${this.escapeHtml(file.path)}">${this.escapeHtml(file.name)}</span>
                <span class="size">${file.isDir ? '—' : this.formatSize(file.size)}</span>
                <span class="actions">
                    <button class="action-btn download-one" title="Download${file.isDir ? ' as zip' : ''}">
                        <svg viewBox="0 0 24 24" width="14" height="14">
                            <path fill="currentColor" d="M19 9h-4V3H9v6H5l7 7 7-7zM5 18v2h14v-2H5z"/>
                        </svg>
                    </button>
                    <button class="action-btn unmark" title="Remove">
                        <svg viewBox="0 0 24 24" width="14" height="14">
                            <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                        </svg>
                    </button>
                </span>
            </div>
        `}).join('');

        // Bind events
        this.markedList.querySelectorAll('.marked-item').forEach(item => {
            const path = item.dataset.path;

            item.querySelector('.download-one').addEventListener('click', (e) => {
                e.stopPropagation();
                this.downloadSingleMarked(path);
            });

            item.querySelector('.unmark').addEventListener('click', (e) => {
                e.stopPropagation();
                this.unmarkFile(path);
            });
        });
    }

    updateMarkedToast() {
        const container = document.getElementById('toast-container');
        let toast = container.querySelector('.marked-toast');

        if (this.markedFiles.length === 0) {
            // Remove toast if no files
            if (toast) {
                toast.classList.add('toast-out');
                setTimeout(() => toast.remove(), 200);
            }
            return;
        }

        const count = this.markedFiles.length;
        const latest = this.markedFiles[this.markedFiles.length - 1];

        if (!toast) {
            // Create toast
            toast = document.createElement('div');
            toast.className = 'marked-toast';
            toast.innerHTML = `
                <svg class="marked-toast-icon" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true">
                    <path fill="currentColor" d="M17 3H7c-1.1 0-2 .9-2 2v16l7-3 7 3V5c0-1.1-.9-2-2-2z"/>
                </svg>
                <div class="marked-toast-content">
                    <div class="marked-toast-count"></div>
                    <div class="marked-toast-latest"></div>
                </div>
                <button class="marked-toast-action" aria-label="Download marked files">Download</button>
            `;

            toast.querySelector('.marked-toast-action').addEventListener('click', () => {
                this.downloadMarkedFiles();
            });

            container.appendChild(toast);
        }

        // Update content
        toast.querySelector('.marked-toast-count').textContent =
            `${count} file${count !== 1 ? 's' : ''} marked`;
        toast.querySelector('.marked-toast-latest').textContent = latest.name;
    }

    updateMarkButtons() {
        // Update mark buttons in the file list to show marked state
        const markedPaths = new Set(this.markedFiles.map(f => f.path));

        this.fileList.querySelectorAll('.file-item').forEach(item => {
            const path = item.dataset.path;
            const isDir = item.dataset.isDir === 'true';
            const markBtn = item.querySelector('.mark-btn');
            if (markBtn) {
                // Skip disabled folder buttons
                if (markBtn.classList.contains('disabled')) return;

                const isMarked = markedPaths.has(path);
                markBtn.classList.toggle('marked', isMarked);
                markBtn.title = isMarked ? 'Unmark' : 'Mark for download';
                // Update icon
                const markIcon = isMarked
                    ? '<path fill="currentColor" d="M17 3H7c-1.1 0-2 .9-2 2v16l7-3 7 3V5c0-1.1-.9-2-2-2z"/>'
                    : '<path fill="currentColor" d="M17 3H7c-1.1 0-2 .9-2 2v16l7-3 7 3V5c0-1.1-.9-2-2-2zm0 15l-5-2.18L7 18V5h10v13z"/>';
                markBtn.innerHTML = `<svg viewBox="0 0 24 24" width="16" height="16">${markIcon}</svg>`;
            }
        });
    }

    isFileMarked(path) {
        return this.markedFiles.some(f => f.path === path);
    }

    // Clipboard Integration
    // =====================

    // Get contentWindows of all pane iframes in the active group.
    // In split views, multiple iframes exist but only one has focus.
    // Clipboard operations are broadcast to all so the focused one can handle them.
    getAllPaneIframes() {
        const group = this.groups.get(this.activeGroupId);
        if (!group || group.paneIds.length === 0) return [];
        const wins = [];
        for (const paneId of group.paneIds) {
            const container = document.getElementById(`pane-${paneId}`);
            if (!container) continue;
            const iframe = container.querySelector('iframe');
            if (iframe?.contentWindow) wins.push(iframe.contentWindow);
        }
        return wins;
    }

    // Write to browser clipboard via pane iframes.
    // Broadcasts to all iframes; the focused one will succeed.
    async writeClipboardViaIframes(text) {
        const iframes = this.getAllPaneIframes();
        for (const win of iframes) {
            win.postMessage({ type: 'clipboard-write', text: text }, '*');
        }

        if (document.hasFocus() && this.terminals.has(this.focusedPaneId)) {
            const fallbackCopy = () => {
                const textarea = document.createElement('textarea');
                textarea.value = text;
                textarea.style.position = 'fixed';
                textarea.style.opacity = '0';
                document.body.appendChild(textarea);
                textarea.select();
                const copied = document.execCommand('copy');
                textarea.remove();
                this.terminals.get(this.focusedPaneId)?.terminal.focus();
                return copied;
            };
            if (navigator.clipboard?.writeText) {
                try {
                    await navigator.clipboard.writeText(text);
                    return true;
                } catch (error) {
                    return fallbackCopy();
                }
            }
            return fallbackCopy();
        }
        return document.hasFocus() && iframes.length > 0;
    }

    connectClipboardEvents() {
        this.startClipboardWebSocket();
    }

    async fetchClipboardAndWrite() {
        const contentResp = await fetch(this.url('/api/clipboard'));
        if (!contentResp.ok) return false;
        const contentType = (contentResp.headers.get('Content-Type') || '').split(';', 1)[0].toLowerCase();
        if (contentType && contentType !== 'text/plain') return true;
        const text = await contentResp.text();
        return this.writeClipboardViaIframes(text);
    }

    clipboardRequestPosition() {
        const entry = this.terminals.get(this.focusedPaneId);
        const screen = entry?.terminal.element?.querySelector('.xterm-screen');
        if (!entry || !screen) {
            return { left: Math.max(12, window.innerWidth / 2 - 110), top: Math.max(12, window.innerHeight / 2 - 30) };
        }
        const terminal = entry.terminal;
        const buffer = terminal.buffer.active;
        const rect = screen.getBoundingClientRect();
        const cellWidth = rect.width / Math.max(terminal.cols, 1);
        const cellHeight = rect.height / Math.max(terminal.rows, 1);
        const row = Math.max(0, Math.min(terminal.rows - 1, buffer.baseY + buffer.cursorY - buffer.viewportY));
        return {
            left: Math.min(window.innerWidth - 232, Math.max(8, rect.left + buffer.cursorX * cellWidth)),
            top: Math.min(window.innerHeight - 72, Math.max(8, rect.top + (row + 1) * cellHeight)),
        };
    }

    clipboardFormData(data) {
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
    }

    showClipboardRequest(request) {
        if (!document.hasFocus() || !request.requestId) return;
        if (this.clipboardRequestPrompt) return;

        const prompt = document.createElement('div');
        prompt.className = 'clipboard-request-prompt';
        prompt.contentEditable = 'true';
        prompt.tabIndex = 0;
        prompt.setAttribute('role', 'textbox');
        prompt.setAttribute('aria-label', 'Paste clipboard contents');
        prompt.textContent = 'Paste clipboard now';
        const position = this.clipboardRequestPosition();
        prompt.style.left = `${position.left}px`;
        prompt.style.top = `${position.top}px`;
        document.body.appendChild(prompt);
        this.clipboardRequestPrompt = prompt;

        const close = () => {
            if (this.clipboardRequestPrompt === prompt) this.clipboardRequestPrompt = null;
            prompt.remove();
            clearTimeout(timer);
            this.terminals.get(this.focusedPaneId)?.terminal.focus();
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
            const formData = this.clipboardFormData(event.clipboardData);
            close();
            try {
                const response = await fetch(this.url(`/api/clipboard/requests/${encodeURIComponent(request.requestId)}`), {
                    method: 'POST',
                    body: formData,
                });
                if (!response.ok && response.status !== 404 && response.status !== 409) {
                    this.toastError('Clipboard request failed');
                }
            } catch (error) {
                this.toastError('Clipboard request failed');
            }
        });
        const timer = setTimeout(close, 15000);
        requestAnimationFrame(() => prompt.focus());
    }

    // Listen for clipboard version changes over WebSocket. The clipboard content
    // still travels through GET /api/clipboard so large payloads are fetched only
    // when a version actually changes.
    startClipboardWebSocket() {
        if (this.connectionMode !== 'active') return;
        this.stopClipboardEvents();
        let knownVersion = -1;
        let pendingVersion = -1;
        let syncing = false;
        const reconnectDelay = 2000;
        const syncClipboard = async version => {
            pendingVersion = Math.max(pendingVersion, version);
            if (syncing || !document.hasFocus()) return;
            syncing = true;
            try {
                while (pendingVersion > knownVersion && document.hasFocus()) {
                    const targetVersion = pendingVersion;
                    if (!await this.fetchClipboardAndWrite()) return;
                    knownVersion = Math.max(knownVersion, targetVersion);
                    if (pendingVersion <= knownVersion) pendingVersion = -1;
                }
            } finally {
                syncing = false;
            }
        };

        const connect = () => {
            if (this.connectionMode !== 'active') return;
            const ws = new WebSocket(this.wsUrl('/api/clipboard/events'));
            this.clipboardSocket = ws;

            ws.onmessage = async (event) => {
                try {
                    const data = JSON.parse(event.data);
                    if (data.type === 'clipboard-request') {
                        this.showClipboardRequest(data);
                        return;
                    }
                    if (data.type !== 'clipboard') return;
                    const version = Number(data.version);
                    if (!Number.isSafeInteger(version)) return;

                    if (knownVersion === -1) {
                        knownVersion = version;
                        return;
                    }
                    if (version === knownVersion) return;
                    await syncClipboard(version);
                } catch (err) {
                    // Ignore malformed messages; reconnect handling is in onclose.
                }
            };

            ws.onclose = () => {
                if (this.clipboardSocket === ws) {
                    this.clipboardSocket = null;
                }
                if (this.connectionMode === 'active' && !this.clipboardReconnectTimer) {
                    this.clipboardReconnectTimer = setTimeout(() => {
                        this.clipboardReconnectTimer = null;
                        connect();
                    }, reconnectDelay);
                }
            };

            ws.onerror = () => {
                ws.close();
            };
        };

        this.clipboardFocusHandler = () => {
            if (pendingVersion > knownVersion) syncClipboard(pendingVersion);
        };
        window.addEventListener('focus', this.clipboardFocusHandler);
        connect();
    }

    stopClipboardEvents() {
        if (this.clipboardReconnectTimer) {
            clearTimeout(this.clipboardReconnectTimer);
            this.clipboardReconnectTimer = null;
        }
        if (this.clipboardFocusHandler) {
            window.removeEventListener('focus', this.clipboardFocusHandler);
            this.clipboardFocusHandler = null;
        }
        const socket = this.clipboardSocket;
        this.clipboardSocket = null;
        socket?.close(1000, 'server connection paused');
    }

    connectDevReload() {
        if (this.connectionMode !== 'active' || this.devReloadSocket) return;
        fetch(this.url('/api/dev-reload'), { method: 'HEAD' })
            .then(response => {
                if (this.connectionMode !== 'active' || (response.status !== 400 && !response.ok)) return;
                const ws = new WebSocket(this.wsUrl('/api/dev-reload'));
                this.devReloadSocket = ws;
                ws.onmessage = event => {
                    if (event.data === 'reload') window.location.reload();
                };
                ws.onclose = () => {
                    if (this.devReloadSocket === ws) this.devReloadSocket = null;
                    if (this.connectionMode === 'active' && !this.devReloadReconnectTimer) {
                        this.devReloadReconnectTimer = setTimeout(() => {
                            this.devReloadReconnectTimer = null;
                            this.connectDevReload();
                        }, 1000);
                    }
                };
                ws.onerror = () => ws.close();
            })
            .catch(() => {});
    }

    stopDevReload() {
        if (this.devReloadReconnectTimer) {
            clearTimeout(this.devReloadReconnectTimer);
            this.devReloadReconnectTimer = null;
        }
        const socket = this.devReloadSocket;
        this.devReloadSocket = null;
        socket?.close(1000, 'server connection paused');
    }

}

// Initialize app
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TerminalMultiplexer();
    window.app.connectDevReload();
});
