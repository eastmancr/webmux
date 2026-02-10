/*
 * Webmux - a browser-based terminal multiplexer
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

class TerminalMultiplexer {
    constructor() {
        // Sessions: individual terminal sessions from the backend
        this.sessions = new Map();

        // Groups: visual groupings of sessions (1-4 sessions per group)
        // Structure: { id, name, sessionIds: [], layout: 'single'|'horizontal'|'vertical'|'grid', expandedQuadrant: null, splitRatio: [] }
        this.groups = new Map();

        // Ordered list of group IDs (for sidebar ordering)
        this.groupOrder = [];

        // Track which group is active
        this.activeGroupId = null;

        // Track which session is focused within a split group (for keybar targeting)
        this.focusedSessionId = null;

        // Track custom names (to know whether to show process name)
        this.customNames = new Set();

        // Drag state for sidebar
        this.draggedSessionId = null;
        this.draggedGroupId = null;

        // Group counter for unique IDs
        this.groupCounter = 0;

        // Track popped out windows: sessionId -> Window object
        this.popoutWindows = new Map();

        // Sidebar collapsed state
        this.sidebarCollapsed = false;

        // Server connection state
        this.serverConnected = true;
        this.connectionCheckInterval = null;

        // Base path for proxy support (detected from current URL)
        this.basePath = this.detectBasePath();

        // Mobile mode detection
        this.mobileMode = false;
        this.mobileModeQuery = window.matchMedia('(max-width: 768px)');
        this.coarsePointerQuery = window.matchMedia('(pointer: coarse)');

        // Mobile swipe navigation state
        this.swipeState = {
            isTracking: false,
            startX: 0,
            startY: 0,
            currentX: 0,
            currentY: 0,
            threshold: 50, // Minimum horizontal distance for swipe
            maxVerticalDeviation: 30 // Maximum vertical movement allowed
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
        this.setupTerminalDragTarget();

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
            await this.loadUIState(); // Load saved UI state from server before loading sessions
            await this.loadSessions();
        }

        if (this.groups.size === 0) {
            document.getElementById('no-session').classList.remove('hidden');
            document.getElementById('keybar').classList.add('hidden');
            document.getElementById('toggle-keybar').classList.remove('active');
        }

        // Apply saved sidebar state
        if (this.sidebarCollapsed) {
            this.sidebar.classList.add('collapsed');
            this.startIconFadeTimer();
        }

        // Start polling for dead sessions (also checks connection)
        this.startSessionHealthCheck();

        // Save state periodically and on changes
        this.startAutoSave();

        // Connect to scratch pad SSE
        this.connectScratchEvents();

        // Connect to marked files SSE
        this.connectMarkedEvents();

        // Connect to clipboard SSE (for wm copy integration)
        this.connectClipboardEvents();
    }

    startSessionHealthCheck() {
        // Check for dead sessions every 500ms
        setInterval(() => this.checkSessionHealth(), 500);
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
        // Update mobile toolbar visibility
        this.updateMobileToolbar();
    }

    exitMobileMode() {
        console.log('[webmux] Exiting mobile mode');
        // Clean up mobile-specific state
        this.closeMobileDrawer();
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
        if (activeGroup && this.mobileSessionName) {
            // Update session name display
            if (activeGroup.sessionIds.length === 1) {
                const session = this.sessions.get(activeGroup.sessionIds[0]);
                this.mobileSessionName.textContent = session ? session.name : 'Terminal';
            } else {
                this.mobileSessionName.textContent = activeGroup.name || `Split (${activeGroup.sessionIds.length})`;
            }
        } else if (this.mobileSessionName) {
            // No active session
            this.mobileSessionName.textContent = 'Sessions';
        }
    }

    showMobileSessionPicker() {
        // Open the mobile drawer to show the session list
        this.openMobileDrawer();
    }

    // Mobile Swipe Navigation
    // =======================

    setupMobileSwipeNavigation() {
        // Only setup swipe navigation if we have the mobile toolbar
        if (!this.mobileBottomToolbar) return;

        // Track touch events on the mobile toolbar
        this.mobileBottomToolbar.addEventListener('touchstart', (e) => this.handleSwipeStart(e), { passive: true });
        this.mobileBottomToolbar.addEventListener('touchmove', (e) => this.handleSwipeMove(e), { passive: true });
        this.mobileBottomToolbar.addEventListener('touchend', (e) => this.handleSwipeEnd(e));

        // Also track mouse events for testing on desktop
        this.mobileBottomToolbar.addEventListener('mousedown', (e) => this.handleSwipeStart(e));
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
    }

    handleSwipeMove(e) {
        if (!this.swipeState.isTracking) return;

        this.swipeState.currentX = this.getClientX(e);
        this.swipeState.currentY = this.getClientY(e);
    }

    handleSwipeEnd(e) {
        if (!this.swipeState.isTracking) return;

        const deltaX = this.swipeState.currentX - this.swipeState.startX;
        const deltaY = Math.abs(this.swipeState.currentY - this.swipeState.startY);

        this.swipeState.isTracking = false;

        // Check if this is a valid horizontal swipe
        if (Math.abs(deltaX) >= this.swipeState.threshold && deltaY <= this.swipeState.maxVerticalDeviation) {
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
            <span class="toast-message">Switched to: ${this.escapeHtml(group.name)}</span>
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

    setServerConnected(connected) {
        // Skip if state hasn't changed
        if (connected === this.serverConnected) {
            return;
        }

        this.serverConnected = connected;

        // Update UI to reflect connection state
        document.body.classList.toggle('server-disconnected', !connected);

        // Update button states
        this.updateActionButtonStates();

        // Show/hide disconnection warning
        if (!connected) {
            this.showDisconnectionWarning();
        } else {
            this.hideDisconnectionWarning();
            // Reload UI state and sessions when reconnected
            this.loadUIState().then(() => this.loadSessions());
        }
    }

    updateActionButtonStates() {
        const disabled = !this.serverConnected;

        // Disable buttons that require server connection
        const serverButtons = [
            this.newSessionBtn,
            this.createFirstBtn,
            this.openUploadBtn,
            this.openDownloadBtn,
        ];

        serverButtons.forEach(btn => {
            if (btn) {
                btn.disabled = disabled;
                btn.classList.toggle('disabled', disabled);
            }
        });

        // Update sidebar action buttons
        this.sessionList?.querySelectorAll('.action-btn').forEach(btn => {
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

    startAutoSave() {
        // Save state every 5 seconds
        setInterval(() => this.saveUIState(), 5000);
        // Also save on page unload
        window.addEventListener('beforeunload', () => this.saveUIState());
    }

    async saveUIState() {
        const state = {
            groupOrder: this.groupOrder,
            groups: Array.from(this.groups.entries()).map(([id, g]) => ({
                id: g.id,
                name: g.name,
                sessionIds: g.sessionIds,
                layout: g.layout,
                expandedQuadrant: g.expandedQuadrant,
                splitRatio: g.splitRatio,
                cellMapping: g.cellMapping
            })),
            activeGroupId: this.activeGroupId,
            sidebarCollapsed: this.sidebar?.classList.contains('collapsed') || false,
            customNames: Array.from(this.customNames),
            groupCounter: this.groupCounter
        };

        try {
            // Save to server (authoritative source)
            await fetch(this.url('/api/ui-state'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(state)
            });
        } catch (e) {
            console.warn('Failed to save UI state to server:', e);
        }
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
                    if (!Array.isArray(g.sessionIds)) return false;
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

            // Validate groupCounter is a safe positive integer
            if (typeof state.groupCounter !== 'number' ||
                !Number.isSafeInteger(state.groupCounter) ||
                state.groupCounter < 0) {
                state.groupCounter = 0;
            }

            // Restore state
            this.savedState = state;
            this.sidebarCollapsed = !!state.sidebarCollapsed;
            this.groupCounter = state.groupCounter;
            this.customNames = new Set(Array.isArray(state.customNames) ? state.customNames.filter(n => typeof n === 'string') : []);
        } catch (e) {
            console.warn('Failed to load UI state from server:', e);
        }
    }

    async checkSessionHealth() {
        try {
            const response = await fetch(this.url('/api/sessions'));

            // Update connection status on successful response
            if (!this.serverConnected) {
                this.setServerConnected(true);
            }

            const sessions = await response.json();

            // Build a map of server sessions and update session data
            const serverSessionIds = new Set();
            let needsRefresh = false;

            for (const session of sessions) {
                serverSessionIds.add(session.id);

                // Update session data (including currentProcess)
                const existing = this.sessions.get(session.id);
                if (existing && existing.currentProcess !== session.currentProcess) {
                    existing.currentProcess = session.currentProcess;
                    needsRefresh = true;
                }
            }

            // Refresh sidebar if process names changed
            if (needsRefresh) {
                this.refreshSidebar();
            }

            // Find and clean up sessions that no longer exist on server
            const currentSessionIds = Array.from(this.sessions.keys());
            for (const sessionId of currentSessionIds) {
                if (!serverSessionIds.has(sessionId)) {
                    // Don't remove sessions that were just created (< 3 seconds ago)
                    // This prevents race conditions where the health check runs before
                    // the server has fully registered the session
                    const session = this.sessions.get(sessionId);
                    const addedAt = session?._addedAt || 0;
                    if (Date.now() - addedAt < 3000) {
                        console.log(`Session ${sessionId} not on server but was just created, waiting...`);
                        continue;
                    }
                    console.log(`Session ${sessionId} no longer exists on server, cleaning up`);
                    this.handleSessionDied(sessionId);
                }
            }
        } catch (error) {
            // Update connection status on failure
            if (this.serverConnected) {
                this.setServerConnected(false);
            }
        }
    }

    refreshSidebar() {
        // Re-render all groups in the sidebar
        for (const [groupId, group] of this.groups) {
            this.updateGroupInSidebar(group);
        }
    }

    handleSessionDied(sessionId) {
        // Guard: only process if we still have this session
        if (!this.sessions.has(sessionId)) {
            return;
        }

        this.sessions.delete(sessionId);
        this.customNames.delete(sessionId);

        // Close popout window if exists
        const popoutWindow = this.popoutWindows.get(sessionId);
        if (popoutWindow && !popoutWindow.closed) {
            popoutWindow.close();
        }
        this.popoutWindows.delete(sessionId);

        // Remove the terminal container from DOM
        this.removeSessionContainer(sessionId);

        // Remove from any group that contains it
        for (const [groupId, group] of this.groups) {
            const sessionIndex = group.sessionIds.indexOf(sessionId);
            if (sessionIndex === -1) continue;

            // Find which pane this session was in (for selecting next in visual order)
            const cm = group.cellMapping || group.sessionIds.map((_, i) => i);
            const paneIndex = cm.indexOf(sessionIndex);

            if (!this.removeSessionFromGroup(group, sessionId)) break;

            if (group.sessionIds.length === 0) {
                // Find the index of this group before removing it
                const groupIndex = this.groupOrder.indexOf(groupId);

                // Remove empty group
                this.groups.delete(groupId);
                this.groupOrder = this.groupOrder.filter(id => id !== groupId);
                document.getElementById(`group-${groupId}`)?.remove();

                if (this.activeGroupId === groupId) {
                    this.activeGroupId = null;
                    // Select the next group in order, or previous if we closed the last one
                    const nextGroupIndex = Math.min(groupIndex, this.groupOrder.length - 1);
                    const nextGroupId = this.groupOrder[nextGroupIndex];
            if (nextGroupId) {
                this.activateGroup(nextGroupId);
            } else {
                this.updateTerminalLayout();
                this.noSessionEl.classList.remove('hidden');
                this.keybar.classList.add('hidden');
                this.keybarToggle.classList.remove('active');
                // Keep expand button visible when no sessions
                this.clearIconFade?.();
            }
                }
            } else {
                this.updateGroupLayout(group);
                this.updateGroupInSidebar(group);
                if (this.activeGroupId === groupId) {
                    this.updateTerminalLayout();
                    // Focus the next session in pane order, or previous if we closed the last
                    const newCm = group.cellMapping || group.sessionIds.map((_, i) => i);
                    const nextPaneIndex = Math.min(paneIndex, newCm.length - 1);
                    const nextSessionIndex = newCm[nextPaneIndex];
                    if (nextSessionIndex !== undefined) {
                        this.focusTerminal(group.sessionIds[nextSessionIndex]);
                    }
                }
            }
            break;
        }
    }

    bindElements() {
        this.sidebar = document.getElementById('sidebar');
        this.sidebarIcons = document.querySelector('.sidebar-icons');
        this.toggleSidebarBtn = document.getElementById('toggle-sidebar');
        this.openSettingsBtn = document.getElementById('open-settings');
        this.sessionList = document.getElementById('session-list');
        this.newSessionBtn = document.getElementById('new-session');
        this.createFirstBtn = document.getElementById('create-first-session');
        this.terminalsContainer = document.getElementById('terminals');
        this.noSessionEl = document.getElementById('no-session');

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
        this.renamingSessionId = null;

        // Settings modal
        this.settingsModal = document.getElementById('settings-modal');
        this.settingsSaveBtn = document.getElementById('settings-save');
        this.settingsResetBtn = document.getElementById('settings-reset');
        this.settingsImportBtn = document.getElementById('settings-import');
        this.settingsExportBtn = document.getElementById('settings-export');
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
        this.mobileSessionPicker = document.getElementById('mobile-session-picker');
        this.mobileSessionName = document.querySelector('.mobile-session-name');
        this.mobileScratchBtn = document.getElementById('mobile-scratch');

    }

    // SECTION: EVENTS

    bindEvents() {
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
            // Don't fade if no sessions active
            if (this.sessions.size === 0) return;
            iconFadeTimeout = setTimeout(() => {
                if (this.sidebar.classList.contains('collapsed') && this.sessions.size > 0) {
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

        // Settings tab switching
        this.settingsModal.querySelectorAll('.settings-tab').forEach(tab => {
            tab.addEventListener('click', () => {
                const tabName = tab.dataset.tab;
                this.settingsModal.querySelectorAll('.settings-tab').forEach(t => t.classList.remove('active'));
                this.settingsModal.querySelectorAll('.settings-panel').forEach(p => p.classList.remove('active'));
                tab.classList.add('active');
                this.settingsModal.querySelector(`[data-panel="${tabName}"]`).classList.add('active');

                // Show/hide theme import/export buttons based on tab
                this.updateThemeActionsVisibility(tabName);
            });
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

        // Session management
        this.newSessionBtn.addEventListener('click', () => this.createNewSessionAndGroup());
        this.createFirstBtn.addEventListener('click', () => this.createNewSessionAndGroup());

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
            if (e.ctrlKey && e.shiftKey && (e.key === 'T' || e.key === 't')) {
                e.preventDefault();
                this.createNewSessionAndGroup();
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
            this.draggedSessionId = null;
            this.hideDragOverlay();
            document.querySelectorAll('.dragging').forEach(el => el.classList.remove('dragging'));
        });

        // Keybar events will be bound dynamically after settings are loaded
        this.bindKeybarEvents();

        // Keybar toggle (in sidebar)
        this.keybarToggle.addEventListener('click', () => {
            // Only toggle if there are active sessions
            if (this.groups.size > 0) {
                this.keybarUserHidden = !this.keybarUserHidden;
                this.keybar.classList.toggle('hidden', this.keybarUserHidden);
                this.keybarToggle.classList.toggle('active', !this.keybarUserHidden);
            }
        });

        // Mobile toolbar event handlers
        if (this.mobileSessionPicker) {
            this.mobileSessionPicker.addEventListener('click', () => {
                this.showMobileSessionPicker();
            });
        }

        // Mobile keybar events will be bound dynamically after settings are loaded
        this.bindMobileKeybarEvents();

        // Mobile utility buttons
        if (this.mobileScratchBtn) {
            this.mobileScratchBtn.addEventListener('click', () => {
                this.toggleScratchPad();
                this.updateScratchButtonState();
            });
        }
    }

    // SECTION: SESSIONS

    // Group & Session Management
    // ==========================

    async loadSessions() {
        try {
            const response = await fetch(this.url('/api/sessions'));
            const serverSessions = await response.json();

            // Clear existing state before loading
            this.sessions.clear();
            this.groups.clear();
            this.groupOrder = [];
            this.sessionList.innerHTML = '';

            // Build map of server sessions
            const serverSessionMap = new Map();
            for (const session of serverSessions) {
                serverSessionMap.set(session.id, session);
                this.sessions.set(session.id, session);
            }

            // Try to restore saved state from server
            if (this.savedState && this.savedState.groups && this.savedState.groups.length > 0) {
                this.reconcileWithSavedState(serverSessionMap);
            } else {
                // No saved state - create a group for each session
                for (const session of serverSessionMap.values()) {
                    const group = this.createGroup([session.id]);
                    this.addGroupToSidebar(group);
                }
            }

            if (this.groups.size > 0) {
                // Restore active group or pick first
                if (this.savedState?.activeGroupId && this.groups.has(this.savedState.activeGroupId)) {
                    this.activateGroup(this.savedState.activeGroupId);
                } else {
                    const firstGroupId = this.groupOrder[0] || this.groups.keys().next().value;
                    if (firstGroupId) this.activateGroup(firstGroupId);
                }
            }

            // Clear saved state after reconciliation
            this.savedState = null;
        } catch (error) {
            console.error('Failed to load sessions:', error);
        }
    }

    reconcileWithSavedState(serverSessionMap) {
        const savedGroups = this.savedState.groups || [];
        const savedOrder = this.savedState.groupOrder || [];
        const usedSessionIds = new Set();

        // Restore groups, filtering out dead sessions
        for (const savedGroup of savedGroups) {
            const validSessionIds = savedGroup.sessionIds.filter(id => serverSessionMap.has(id));

            if (validSessionIds.length === 0) continue;

            validSessionIds.forEach(id => usedSessionIds.add(id));

            // Recreate group with saved properties
            const sameSessionCount = validSessionIds.length === savedGroup.sessionIds.length;
            const group = {
                id: savedGroup.id,
                name: savedGroup.name,
                sessionIds: validSessionIds,
                layout: sameSessionCount ? savedGroup.layout : this.getDefaultLayout(validSessionIds.length),
                expandedQuadrant: savedGroup.expandedQuadrant,
                splitRatio: sameSessionCount ? savedGroup.splitRatio : this.getDefaultSplitRatio(validSessionIds.length),
                cellMapping: sameSessionCount ? savedGroup.cellMapping : null
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

        // Create groups for any sessions not in saved state (new sessions)
        for (const [sessionId, session] of serverSessionMap) {
            if (!usedSessionIds.has(sessionId)) {
                const group = this.createGroup([sessionId]);
                this.groupOrder.push(group.id);
            }
        }

        // Render sidebar in order
        for (const groupId of this.groupOrder) {
            const group = this.groups.get(groupId);
            if (group) this.addGroupToSidebar(group);
        }
    }

    createGroup(sessionIds, name = null) {
        const id = `group-${++this.groupCounter}`;
        const group = {
            id,
            name: name || this.generateGroupName(sessionIds),
            sessionIds: [...sessionIds],
            layout: sessionIds.length === 1 ? 'single' : this.getDefaultLayout(sessionIds.length),
            expandedQuadrant: null,
            splitRatio: this.getDefaultSplitRatio(sessionIds.length),
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

    // Remove a session from a group, updating cellMapping appropriately
    removeSessionFromGroup(group, sessionId) {
        const idx = group.sessionIds.indexOf(sessionId);
        if (idx === -1) return false;

        // Clear focused session if it's the one being removed
        if (this.focusedSessionId === sessionId) {
            this.focusedSessionId = null;
        }

        group.sessionIds.splice(idx, 1);

        // Update cellMapping: remove the session's pane and adjust remaining indices
        if (group.cellMapping) {
            const paneIdx = group.cellMapping.indexOf(idx);
            const newMapping = group.cellMapping
                .filter((_, i) => i !== paneIdx)
                .map(sessionIdx => sessionIdx > idx ? sessionIdx - 1 : sessionIdx);
            group.cellMapping = newMapping.length > 0 ? newMapping : null;
        }

        return true;
    }

    updateGroupLayout(group) {
        const count = group.sessionIds.length;
        group.layout = this.getDefaultLayout(count);
        group.splitRatio = this.getDefaultSplitRatio(count);

        // Reset expandedQuadrant for 3-pane if not already set
        if (count === 3 && !group.expandedQuadrant) {
            group.expandedQuadrant = 'bottom';
        } else if (count !== 3) {
            group.expandedQuadrant = null;
        }
    }

    generateGroupName(sessionIds) {
        if (sessionIds.length === 1) {
            const session = this.sessions.get(sessionIds[0]);
            return session ? session.name : 'Terminal';
        }
        return `Split (${sessionIds.length})`;
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

    async createNewSessionAndGroup() {
        if (!this.serverConnected) {
            this.toastError('Cannot create terminal: server disconnected');
            return;
        }
        const session = await this.createSession();
        if (session) {
            const group = this.createGroup([session.id]);
            this.addGroupToSidebar(group);
            this.activateGroup(group.id);
            // noSessionEl and keybar are handled by activateGroup
        }
    }

    async createSession(name = '') {
        try {
            const response = await fetch(this.url('/api/sessions'), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name })
            });

            if (!response.ok) throw new Error('Failed to create session');

            const session = await response.json();

            // Check for duplicate session ID (shouldn't happen, but guard against it)
            if (this.sessions.has(session.id)) {
                console.warn(`Session ${session.id} already exists, skipping duplicate`);
                return null;
            }

            // Mark when this session was added to the frontend (for race condition protection)
            session._addedAt = Date.now();
            this.sessions.set(session.id, session);
            return session;
        } catch (error) {
            console.error('Failed to create session:', error);
            this.toastError('Failed to create terminal session. Is the server running?');
            return null;
        }
    }

    async closeSession(sessionId) {
        try {
            await fetch(this.url(`/api/sessions/${sessionId}`), { method: 'DELETE' });
            this.sessions.delete(sessionId);
            this.customNames.delete(sessionId);

            // Close popout window if exists
            const popoutWindow = this.popoutWindows.get(sessionId);
            if (popoutWindow && !popoutWindow.closed) {
                popoutWindow.close();
            }
            this.popoutWindows.delete(sessionId);

            this.removeSessionContainer(sessionId);

            for (const [groupId, group] of this.groups) {
                const sessionIndex = group.sessionIds.indexOf(sessionId);
                if (sessionIndex === -1) continue;

                // Find which pane this session was in (for selecting next in visual order)
                const cm = group.cellMapping || group.sessionIds.map((_, i) => i);
                const paneIndex = cm.indexOf(sessionIndex);

                if (!this.removeSessionFromGroup(group, sessionId)) break;

                if (group.sessionIds.length === 0) {
                    this.closeGroup(groupId);
                } else {
                    this.updateGroupLayout(group);
                    this.updateGroupInSidebar(group);
                    if (this.activeGroupId === groupId) {
                        this.updateTerminalLayout();
                        // Focus the next session in pane order, or previous if we closed the last
                        const newCm = group.cellMapping || group.sessionIds.map((_, i) => i);
                        const nextPaneIndex = Math.min(paneIndex, newCm.length - 1);
                        const nextSessionIndex = newCm[nextPaneIndex];
                        if (nextSessionIndex !== undefined) {
                            this.focusTerminal(group.sessionIds[nextSessionIndex]);
                        }
                    }
                }
                break;
            }
        } catch (error) {
            console.error('Failed to close session:', error);
        }
    }

    // Send keys to a session via the terminal key API
    // payload can be:
    //   - Simple: { keys: ['C-c', 'C-d'] }
    //   - Extended: { sequence: [{type: 'key', value: 'C-c'}, {type: 'text', value: 'hello\n'}] }
    async sendKeysToSession(sessionId, payload) {
        try {
            const response = await fetch(this.url(`/api/sessions/${sessionId}/keys`), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to send keys');
            }

            return true;
        } catch (error) {
            console.error('Failed to send keys:', error);
            return false;
        }
    }

    // Send keys to the currently active session
    async sendKeysToActiveSession(payload) {
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.sessionIds.length === 0) {
            console.warn('No active session to send keys to');
            return false;
        }

        // Send to the focused session within the group, falling back to the first
        const activeSessionId = (this.focusedSessionId && activeGroup.sessionIds.includes(this.focusedSessionId))
            ? this.focusedSessionId
            : activeGroup.sessionIds[0];
        return this.sendKeysToSession(activeSessionId, payload);
    }

    closeGroup(groupId) {
        const group = this.groups.get(groupId);
        if (!group) return;

        // Find the index of this group before removing it
        const groupIndex = this.groupOrder.indexOf(groupId);

        for (const sessionId of group.sessionIds) {
            fetch(this.url(`/api/sessions/${sessionId}`), { method: 'DELETE' }).catch(() => {});
            this.sessions.delete(sessionId);
            this.customNames.delete(sessionId);

            // Close popout window if exists
            const popoutWindow = this.popoutWindows.get(sessionId);
            if (popoutWindow && !popoutWindow.closed) {
                popoutWindow.close();
            }
            this.popoutWindows.delete(sessionId);

            this.removeSessionContainer(sessionId);
        }

        this.groups.delete(groupId);
        this.groupOrder = this.groupOrder.filter(id => id !== groupId);
        document.getElementById(`group-${groupId}`)?.remove();

        if (this.activeGroupId === groupId) {
            this.activeGroupId = null;
            // Select the next group in order, or previous if we closed the last one
            const nextGroupIndex = Math.min(groupIndex, this.groupOrder.length - 1);
            const nextGroupId = this.groupOrder[nextGroupIndex];
            if (nextGroupId) {
                this.activateGroup(nextGroupId);
            } else {
                this.focusedSessionId = null;
                this.updateTerminalLayout();
                this.noSessionEl.classList.remove('hidden');
                this.keybar.classList.add('hidden');
                this.keybarToggle.classList.remove('active');
                this.clearIconFade?.();
            }
        }

        this.saveUIState();
    }

    breakOutSession(sessionId, groupId) {
        const group = this.groups.get(groupId);
        if (!group || group.sessionIds.length <= 1) return;

        if (!this.removeSessionFromGroup(group, sessionId)) return;

        this.updateGroupLayout(group);
        this.updateGroupInSidebar(group);

        const newGroup = this.createGroup([sessionId]);
        this.addGroupToSidebar(newGroup);

        this.activateGroup(newGroup.id);
    }

    breakOutAllSessions(groupId) {
        const group = this.groups.get(groupId);
        if (!group || group.sessionIds.length <= 1) return;

        const sessionIds = [...group.sessionIds];

        this.groups.delete(groupId);
        document.getElementById(`group-${groupId}`)?.remove();

        let firstNewGroup = null;
        for (const sessionId of sessionIds) {
            const newGroup = this.createGroup([sessionId]);
            this.addGroupToSidebar(newGroup);
            if (!firstNewGroup) firstNewGroup = newGroup;
        }

        if (this.activeGroupId === groupId && firstNewGroup) {
            this.activeGroupId = null;
            this.activateGroup(firstNewGroup.id);
        }
    }

    // SECTION: SIDEBAR

    // Sidebar UI
    // ==========

    addGroupToSidebar(group) {
        const container = document.createElement('div');
        container.id = `group-${group.id}`;
        container.className = 'group-container';
        container.innerHTML = this.renderGroupSidebarHTML(group);

        this.bindGroupEvents(container, group);
        this.sessionList.appendChild(container);
    }

    updateGroupInSidebar(group) {
        const container = document.getElementById(`group-${group.id}`);
        if (!container) return;

        // Don't re-render if we're in the middle of renaming
        if (this.renamingSessionId && group.sessionIds.includes(this.renamingSessionId)) {
            return;
        }

        container.innerHTML = this.renderGroupSidebarHTML(group);
        this.bindGroupEvents(container, group);
    }

    renderGroupSidebarHTML(group) {
        // Filter out any session IDs that no longer exist
        const validSessionIds = group.sessionIds.filter(id => this.sessions.has(id));
        if (validSessionIds.length !== group.sessionIds.length) {
            // Update group with only valid sessions
            group.sessionIds = validSessionIds;
            // Reset cellMapping since we can't easily remap after arbitrary removals
            group.cellMapping = null;
            if (validSessionIds.length === 0) {
                // Schedule group removal (can't do it during render)
                setTimeout(() => this.closeGroup(group.id), 0);
                return '<div class="session-item">Closing...</div>';
            }
            this.updateGroupLayout(group);
        }

        const isMulti = validSessionIds.length > 1;

        if (!isMulti) {
            const session = this.sessions.get(validSessionIds[0]);
            const displayName = this.getSessionDisplayName(session);
            const processName = this.getSessionProcessDisplay(session);
            const processHtml = processName ? `<span class="process-name"> · ${this.escapeHtml(processName)}</span>` : '';
            const isRenaming = this.renamingSessionId === session?.id;

            const nameHtml = isRenaming
                ? `<input type="text" class="inline-rename-input" value="${this.escapeHtml(displayName)}" data-session-id="${session?.id}">`
                : `<span class="name">${this.escapeHtml(displayName)}${processHtml}</span>`;

            return `
                <div class="session-item ${this.activeGroupId === group.id ? 'active' : ''}"
                     data-group-id="${group.id}" data-session-id="${session?.id}" draggable="${!isRenaming}"
                     role="button" aria-label="Terminal session: ${this.escapeHtml(displayName)}">
                    <svg class="icon" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
                        <path fill="currentColor" d="M20 19V7H4v12h16m0-16a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h16m-7 14v-2h5v2h-5m-3.42-4L5.57 9H8.4l3.3 3.3c.39.39.39 1.03 0 1.42L8.42 17H5.59l4-4z"/>
                    </svg>
                    ${nameHtml}
                    <div class="actions">
                        <button class="action-btn popout" title="Pop out" data-session-id="${session?.id}" aria-label="Pop out terminal">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                            </svg>
                        </button>
                        <button class="action-btn rename" title="Rename" data-session-id="${session?.id}" aria-label="Rename terminal">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M20.71 7.04c.39-.39.39-1.04 0-1.41l-2.34-2.34c-.37-.39-1.02-.39-1.41 0l-1.84 1.83 3.75 3.75M3 17.25V21h3.75L17.81 9.93l-3.75-3.75L3 17.25z"/>
                            </svg>
                        </button>
                        <button class="action-btn close" title="Close" data-group-id="${group.id}" aria-label="Close terminal">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                            </svg>
                        </button>
                    </div>
                </div>
            `;
        }

        const sessionItems = validSessionIds.map(sid => {
            const session = this.sessions.get(sid);
            const displayName = this.getSessionDisplayName(session);
            const processName = this.getSessionProcessDisplay(session);
            const processHtml = processName ? `<span class="process-name"> · ${this.escapeHtml(processName)}</span>` : '';
            return `
                <div class="session-item sub-item" data-session-id="${sid}" data-group-id="${group.id}" draggable="true"
                     role="button" aria-label="Terminal session: ${this.escapeHtml(displayName)}">
                    <svg class="icon" viewBox="0 0 24 24" width="16" height="16" aria-hidden="true">
                        <path fill="currentColor" d="M20 19V7H4v12h16m0-16a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h16m-7 14v-2h5v2h-5m-3.42-4L5.57 9H8.4l3.3 3.3c.39.39.39 1.03 0 1.42L8.42 17H5.59l4-4z"/>
                    </svg>
                    <span class="name">${this.escapeHtml(displayName)}${processHtml}</span>
                    <div class="actions">
                        <button class="action-btn popout" title="Pop out" data-session-id="${sid}" aria-label="Pop out terminal">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                            </svg>
                        </button>
                        <button class="action-btn breakout" title="Break out from split" data-session-id="${sid}" data-group-id="${group.id}" aria-label="Break out from split">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M4 4h7V2H4a2 2 0 0 0-2 2v7h2V4zm6 12l-4 4h3v2H2v-7h2v3l4-4 2 2zm8-6l4-4v3h2V2h-7v2h3l-4 4 2 2z"/>
                            </svg>
                        </button>
                        <button class="action-btn close" title="Close" data-session-id="${sid}" aria-label="Close terminal">
                            <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                                <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                            </svg>
                        </button>
                    </div>
                </div>
            `;
        }).join('');

        return `
            <div class="group-header compact ${this.activeGroupId === group.id ? 'active' : ''}" data-group-id="${group.id}" draggable="true"
                 role="button" aria-label="Terminal group: ${this.escapeHtml(group.name)}">
                <svg class="icon" viewBox="0 0 24 24" width="14" height="14" aria-hidden="true">
                    <path fill="currentColor" d="M3 5v14h18V5H3zm8 12H5v-5h6v5zm0-7H5V5h6v5zm8 7h-6v-5h6v5zm0-7h-6V5h6v5z"/>
                </svg>
                <span class="name">Split</span>
                <div class="actions">
                    <button class="action-btn breakout-all" title="Break out all" data-group-id="${group.id}" aria-label="Break out all terminals">
                        <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
                            <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
                        </svg>
                    </button>
                    <button class="action-btn close" title="Close all" data-group-id="${group.id}" aria-label="Close all terminals in group">
                        <svg viewBox="0 0 24 24" width="12" height="12" aria-hidden="true">
                            <path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z"/>
                        </svg>
                    </button>
                </div>
            </div>
            <div class="group-sessions">
                ${sessionItems}
            </div>
        `;
    }

    bindGroupEvents(container, group) {
        const header = container.querySelector('.group-header');
        if (header) {
            header.addEventListener('click', (e) => {
                if (!e.target.closest('.action-btn')) {
                    // Focus first session when clicking group header
                    this.activateGroup(group.id, group.sessionIds[0]);
                }
            });
        }

        const singleItem = container.querySelector('.session-item:not(.sub-item)');
        if (singleItem && !header) {
            singleItem.addEventListener('click', (e) => {
                if (!e.target.closest('.action-btn') && !e.target.closest('.inline-rename-input')) {
                    const sessionId = singleItem.dataset.sessionId;
                    this.activateGroup(group.id, sessionId);
                }
            });
        }

        container.querySelectorAll('.session-item.sub-item').forEach(item => {
            item.addEventListener('click', (e) => {
                if (!e.target.closest('.action-btn')) {
                    const sessionId = item.dataset.sessionId;
                    this.activateGroup(group.id, sessionId);
                }
            });

            item.addEventListener('mouseenter', () => {
                const sessionId = item.dataset.sessionId;
                this.highlightTerminalInGroup(sessionId, true);
            });
            item.addEventListener('mouseleave', () => {
                const sessionId = item.dataset.sessionId;
                this.highlightTerminalInGroup(sessionId, false);
            });
        });

        container.querySelectorAll('[draggable="true"]').forEach(item => {
            item.addEventListener('dragstart', (e) => {
                this.draggedSessionId = item.dataset.sessionId;
                this.draggedGroupId = item.dataset.groupId;
                item.classList.add('dragging');
                e.dataTransfer.effectAllowed = 'move';
                e.dataTransfer.setData('text/plain', this.draggedSessionId || this.draggedGroupId);
            });
            item.addEventListener('dragend', () => {
                item.classList.remove('dragging');
                this.draggedSessionId = null;
                this.draggedGroupId = null;
                this.hideDragOverlay();
                this.clearSidebarDropIndicators();
            });
        });

        container.addEventListener('dragover', (e) => {
            if (!this.draggedSessionId && !this.draggedGroupId) return;
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
                const sessionId = btn.dataset.sessionId;
                const groupId = btn.dataset.groupId;
                if (sessionId) {
                    this.closeSession(sessionId);
                } else if (groupId) {
                    this.closeGroup(groupId);
                }
            });
        });

        container.querySelectorAll('.action-btn.rename').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.startInlineRename(btn.dataset.sessionId);
            });
        });

        container.querySelectorAll('.action-btn.breakout').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.breakOutSession(btn.dataset.sessionId, btn.dataset.groupId);
            });
        });

        container.querySelectorAll('.action-btn.breakout-all').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.breakOutAllSessions(btn.dataset.groupId);
            });
        });

        container.querySelectorAll('.action-btn.popout').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.popOutSession(btn.dataset.sessionId);
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
                    this.finishInlineRename(renameInput.value.trim());
                } else if (e.key === 'Escape') {
                    e.preventDefault();
                    this.cancelInlineRename();
                }
                e.stopPropagation();
            });

            renameInput.addEventListener('blur', () => {
                // Small delay to allow click events to process first
                setTimeout(() => {
                    if (this.renamingSessionId) {
                        this.finishInlineRename(renameInput.value.trim());
                    }
                }, 100);
            });

            renameInput.addEventListener('click', (e) => {
                e.stopPropagation();
            });
        }
    }

    getSessionDisplayName(session) {
        if (!session) return 'Terminal';
        return session.name;
    }

    getSessionProcessDisplay(session) {
        if (!session || !session.currentProcess) return '';
        return session.currentProcess;
    }

    highlightTerminalInGroup(sessionId, highlight) {
        const container = document.getElementById(`terminal-${sessionId}`);
        if (container) {
            container.classList.toggle('highlighted', highlight);
        }
    }

    clearAllHighlights() {
        document.querySelectorAll('.terminal-container.highlighted').forEach(el => {
            el.classList.remove('highlighted');
        });
    }

    // Inline Rename
    // =============

    startInlineRename(sessionId) {
        const session = this.sessions.get(sessionId);
        if (!session) return;

        this.renamingSessionId = sessionId;

        // Find the group containing this session and re-render
        for (const group of this.groups.values()) {
            if (group.sessionIds.includes(sessionId)) {
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
        if (!this.renamingSessionId) return;

        const sessionId = this.renamingSessionId;
        this.renamingSessionId = null;

        if (newName) {
            try {
                await fetch(this.url(`/api/sessions/${sessionId}`), {
                    method: 'PATCH',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ name: newName })
                });

                const session = this.sessions.get(sessionId);
                if (session) {
                    session.name = newName;
                    this.customNames.add(sessionId);
                }
            } catch (error) {
                console.error('Failed to rename session:', error);
            }
        }

        // Re-render the sidebar item
        for (const group of this.groups.values()) {
            if (group.sessionIds.includes(sessionId)) {
                this.updateGroupInSidebar(group);
                break;
            }
        }
    }

    cancelInlineRename() {
        if (!this.renamingSessionId) return;

        const sessionId = this.renamingSessionId;
        this.renamingSessionId = null;

        // Re-render without the input
        for (const group of this.groups.values()) {
            if (group.sessionIds.includes(sessionId)) {
                this.updateGroupInSidebar(group);
                break;
            }
        }
    }

    // Popout Management
    // =================

    popOutSession(sessionId) {
        const session = this.sessions.get(sessionId);
        if (!session) return;

        if (this.popoutWindows.has(sessionId)) {
            const existingWindow = this.popoutWindows.get(sessionId);
            if (existingWindow && !existingWindow.closed) {
                existingWindow.focus();
                return;
            }
        }

        const container = document.getElementById(`terminal-${sessionId}`);
        if (!container) return;

        const width = 800;
        const height = 600;
        const left = window.screenX + 50;
        const top = window.screenY + 50;

        const popoutUrl = this.url(`/t/${session.id}/`);
        const popoutWindow = window.open(
            popoutUrl,
            `terminal-${sessionId}`,
            `width=${width},height=${height},left=${left},top=${top},menubar=no,toolbar=no,location=no,status=no`
        );

        if (popoutWindow) {
            this.popoutWindows.set(sessionId, popoutWindow);
            container.classList.add('popped-out');

            const checkClosed = setInterval(() => {
                if (popoutWindow.closed) {
                    clearInterval(checkClosed);
                    this.popInSession(sessionId);
                }
            }, 500);
        }
    }

    popInSession(sessionId) {
        const container = document.getElementById(`terminal-${sessionId}`);
        if (!container) return;

        const popoutWindow = this.popoutWindows.get(sessionId);
        if (popoutWindow && !popoutWindow.closed) {
            popoutWindow.close();
        }
        this.popoutWindows.delete(sessionId);

        container.classList.remove('popped-out');

        const iframe = container.querySelector('iframe');
        if (iframe) {
            // Reset to the correct terminal URL (not whatever the iframe might have navigated to)
            const correctSrc = this.url(`/t/${sessionId}/`);
            iframe.src = '';
            setTimeout(() => { iframe.src = correctSrc; }, 50);
        }
    }

    // Terminal Rendering
    // ==================

    activateGroup(groupId, focusSessionId = null) {
        const group = this.groups.get(groupId);
        if (!group) return;

        this.clearAllHighlights();

        document.querySelectorAll('.group-container').forEach(el => {
            const gid = el.id.replace('group-', '');
            const isActive = gid === groupId;
            el.querySelector('.group-header, .session-item')?.classList.toggle('active', isActive);
        });

        this.activeGroupId = groupId;
        this.updateTerminalLayout();
        this.noSessionEl.classList.add('hidden');
        // Show keybar unless user manually hid it
        this.keybar.classList.toggle('hidden', this.keybarUserHidden);
        this.keybarToggle.classList.toggle('active', !this.keybarUserHidden);

        // Update mobile toolbar
        this.updateMobileToolbar();

        // Focus the specified session, or the first one if not specified
        const sessionToFocus = focusSessionId && group.sessionIds.includes(focusSessionId)
            ? focusSessionId
            : group.sessionIds[0];
        this.focusTerminal(sessionToFocus);
    }

    focusTerminal(sessionId) {
        const container = document.getElementById(`terminal-${sessionId}`);
        if (!container) return;

        // Track focused session for keybar targeting in split groups
        this.focusedSessionId = sessionId;

        // Don't focus if popped out
        if (container.classList.contains('popped-out')) return;

        const iframe = container.querySelector('iframe');
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
                // Cross-origin fallback (shouldn't happen since we proxy ttyd)
                iframe.focus();
            }
        }, 100);
    }

    createSessionContainer(session) {
        const container = document.createElement('div');
        container.id = `terminal-${session.id}`;
        container.className = 'terminal-container loading';
        container.dataset.sessionId = session.id;

        // Loading overlay shown while terminal connects
        const loadingOverlay = document.createElement('div');
        loadingOverlay.className = 'terminal-loading';
        loadingOverlay.setAttribute('role', 'status');
        loadingOverlay.setAttribute('aria-live', 'polite');
        loadingOverlay.innerHTML = `
            <div class="terminal-loading-spinner" aria-hidden="true"></div>
            <p>Connecting...</p>
        `;

        const iframe = document.createElement('iframe');
        iframe.src = this.url(`/t/${session.id}/`);
        iframe.className = 'terminal-iframe';
        iframe.title = `Terminal session: ${session.name}`;
        iframe.allow = 'clipboard-read; clipboard-write';

        // Listen for iframe navigation (happens when session dies and ttyd redirects)
        // Only trigger on subsequent loads (not the initial load)
        let initialLoad = true;
        iframe.addEventListener('load', () => {
            if (initialLoad) {
                initialLoad = false;
                // Remove loading state once terminal loads
                container.classList.remove('loading');
                return;
            }
            // iframe reloaded - session likely died, check immediately
            this.checkSessionHealth();
        });

        // Also listen for errors to show loading failed
        iframe.addEventListener('error', () => {
            if (initialLoad) {
                loadingOverlay.querySelector('p').textContent = 'Failed to connect';
            }
        });

        const placeholder = document.createElement('div');
        placeholder.className = 'popout-placeholder hidden';
        placeholder.setAttribute('role', 'status');
        placeholder.innerHTML = `
            <svg viewBox="0 0 24 24" width="48" height="48" aria-hidden="true">
                <path fill="currentColor" d="M19 19H5V5h7V3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7h-2v7zM14 3v2h3.59l-9.83 9.83 1.41 1.41L19 6.41V10h2V3h-7z"/>
            </svg>
            <p>Terminal popped out</p>
            <button class="btn btn-secondary pop-back-in" data-session-id="${session.id}" aria-label="Pop terminal back into browser window">Pop back in</button>
        `;

        placeholder.querySelector('.pop-back-in').addEventListener('click', () => {
            this.popInSession(session.id);
        });

        // Focus this terminal when clicking on the container (gaps/borders)
        container.addEventListener('click', () => {
            this.focusTerminal(session.id);
        });

        container.appendChild(loadingOverlay);
        container.appendChild(iframe);
        container.appendChild(placeholder);
        this.terminalsContainer.appendChild(container);

        return container;
    }

    getSessionContainer(sessionId) {
        let container = document.getElementById(`terminal-${sessionId}`);
        if (!container) {
            const session = this.sessions.get(sessionId);
            if (session) {
                container = this.createSessionContainer(session);
            }
        }
        return container;
    }

    removeSessionContainer(sessionId) {
        const container = document.getElementById(`terminal-${sessionId}`);
        if (container) {
            container.remove();
        }
    }

    updateTerminalLayout(keepControlVisible = false) {
        const activeGroup = this.groups.get(this.activeGroupId);

        document.querySelectorAll('.terminal-container').forEach(el => {
            el.classList.remove('visible', 'pane-0', 'pane-1', 'pane-2', 'pane-3', 'expanded', 'expanded-top', 'expanded-left');
            el.style.gridArea = '';
        });

        document.querySelectorAll('.split-divider').forEach(el => el.remove());
        document.getElementById('divider-control')?.remove();
        this.ensureResizeOverlay();

        if (!activeGroup) {
            this.terminalsContainer.className = 'layout-single';
            this.terminalsContainer.style.gridTemplateColumns = '';
            this.terminalsContainer.style.gridTemplateRows = '';
            return;
        }

        const sessionCount = activeGroup.sessionIds.length;
        const layout = activeGroup.layout;
        const ratio = activeGroup.splitRatio || this.getDefaultSplitRatio(sessionCount);

        // For 3-pane, add modifier class for expansion direction
        let containerClass = `layout-${layout}`;
        const expandDir = activeGroup.expandedQuadrant; // 'bottom', 'top', 'left', 'right' or legacy number

        if (sessionCount === 3 && layout === 'grid') {
            // Convert legacy numeric values
            let dir = expandDir;
            if (dir === 2 || dir === undefined || dir === null) dir = 'bottom';
            if (dir === 1) dir = 'right';

            if (dir === 'top') containerClass += ' top-wide';
            else if (dir === 'left') containerClass += ' left-wide';
        }

        this.terminalsContainer.className = containerClass;
        this.applyGridTemplate(layout, ratio, sessionCount);

        // Force reflow to ensure grid template is applied before adding dividers
        this.terminalsContainer.offsetHeight;

        // cellMapping maps pane positions to session indices
        // If not set, use identity mapping (session 0 -> pane 0, etc.)
        const cellMapping = activeGroup.cellMapping || activeGroup.sessionIds.map((_, i) => i);

        activeGroup.sessionIds.forEach((sessionId, sessionIndex) => {
            const container = this.getSessionContainer(sessionId);
            if (container) {
                // Find which pane this session should occupy
                const paneIndex = cellMapping.indexOf(sessionIndex);
                container.classList.add('visible', `pane-${paneIndex}`);

                if (sessionCount === 3) {
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
            }
        });

        this.createDividers(layout, sessionCount, expandDir, keepControlVisible);
    }

    ensureResizeOverlay() {
        if (!document.getElementById('resize-overlay')) {
            const overlay = document.createElement('div');
            overlay.id = 'resize-overlay';
            this.terminalsContainer.appendChild(overlay);
        }
    }

    applyGridTemplate(layout, ratio, sessionCount) {
        const gap = 'var(--split-gap)';

        switch (layout) {
            case 'single':
                this.terminalsContainer.style.gridTemplateColumns = '1fr';
                this.terminalsContainer.style.gridTemplateRows = '1fr';
                break;
            case 'horizontal':
                const hRatio = ratio[0];
                this.terminalsContainer.style.gridTemplateColumns = `${hRatio}fr ${gap} ${1 - hRatio}fr`;
                this.terminalsContainer.style.gridTemplateRows = '1fr';
                break;
            case 'vertical':
                const vRatio = ratio[0];
                this.terminalsContainer.style.gridTemplateColumns = '1fr';
                this.terminalsContainer.style.gridTemplateRows = `${vRatio}fr ${gap} ${1 - vRatio}fr`;
                break;
            case 'grid':
                const colRatio = ratio[0];
                const rowRatio = ratio[1];
                this.terminalsContainer.style.gridTemplateColumns = `${colRatio}fr ${gap} ${1 - colRatio}fr`;
                this.terminalsContainer.style.gridTemplateRows = `${rowRatio}fr ${gap} ${1 - rowRatio}fr`;
                break;
        }
    }

    createDividers(layout, sessionCount, expandedQuadrant = 2, keepControlVisible = false) {
        if (layout === 'single') return;

        if (layout === 'horizontal') {
            const divider = document.createElement('div');
            divider.className = 'split-divider split-divider-h';
            divider.style.gridColumn = '2';
            divider.style.gridRow = '1';
            divider.dataset.axis = 'horizontal';
            divider.dataset.index = '0';
            this.terminalsContainer.appendChild(divider);
            this.bindDividerEvents(divider);
            this.createDividerControl('2-pane', null, keepControlVisible);
        } else if (layout === 'vertical') {
            const divider = document.createElement('div');
            divider.className = 'split-divider split-divider-v';
            divider.style.gridColumn = '1';
            divider.style.gridRow = '2';
            divider.dataset.axis = 'vertical';
            divider.dataset.index = '0';
            this.terminalsContainer.appendChild(divider);
            this.bindDividerEvents(divider);
            this.createDividerControl('2-pane', null, keepControlVisible);
        } else if (layout === 'grid') {
            const is3Pane = sessionCount === 3;

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
            this.terminalsContainer.appendChild(hDivider);
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
            this.terminalsContainer.appendChild(vDivider);
            this.bindDividerEvents(vDivider);

            this.createDividerControl(is3Pane ? '3-pane' : '4-pane', expandedQuadrant, keepControlVisible);
        }
    }

    createDividerControl(mode, expandedQuadrant = 2, showImmediately = false) {
        // Remove existing control
        document.getElementById('divider-control')?.remove();

        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.sessionIds.length < 2) return;

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

            this.terminalsContainer.appendChild(control);
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

        this.terminalsContainer.appendChild(control);
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
        const ratio = activeGroup.splitRatio || [0.5, 0.5];
        const rect = this.terminalsContainer.getBoundingClientRect();

        // If container isn't laid out yet, retry after a frame
        if (rect.width === 0 || rect.height === 0) {
            requestAnimationFrame(() => this.positionDividerControl(showImmediately));
            return;
        }

        let x, y;

        if (layout === 'horizontal') {
            x = rect.width * ratio[0];
            y = rect.height / 2;
        } else if (layout === 'vertical') {
            x = rect.width / 2;
            y = rect.height * ratio[0];
        } else if (layout === 'grid') {
            // Position at the crux of dividers
            x = rect.width * ratio[0];
            y = rect.height * ratio[1];
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

        const count = activeGroup.sessionIds.length;
        // cellMapping maps pane positions to session indices
        // Default is identity: [0,1,2,3] meaning session 0 in pane 0, etc.
        const cm = activeGroup.cellMapping || activeGroup.sessionIds.map((_, i) => i);

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

            // 3-pane layout changes - remap so terminals only move cardinally
            //
            // Each 3-pane layout has one wide pane and two small panes.
            // When switching layouts, we pick the small terminal closest to the
            // target edge to become wide. Terminals only move cardinally.
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
                        // 1. The small terminal on the target edge becomes wide
                        // 2. The old wide terminal contracts to the opposite edge
                        // 3. The other small terminal shifts cardinally (not diagonally)
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

        this.updateTerminalLayout(true); // keepControlVisible = true
        this.pinDividerControl(); // Keep menu open after action
        this.updateGroupInSidebar(activeGroup);
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

        const containerRect = this.terminalsContainer.getBoundingClientRect();
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
            this.applyGridTemplate(activeGroup.layout, activeGroup.splitRatio, activeGroup.sessionIds.length);
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

    setupTerminalDragTarget() {
        document.addEventListener('dragstart', () => {
            setTimeout(() => {
                if (this.draggedSessionId) {
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

        if (!activeGroup || activeGroup.sessionIds.length >= 4) {
            return;
        }

        if (this.draggedSessionId && activeGroup.sessionIds.includes(this.draggedSessionId)) {
            return;
        }

        let overlay = document.getElementById('drag-capture-overlay');
        if (!overlay) {
            overlay = document.createElement('div');
            overlay.id = 'drag-capture-overlay';
            this.terminalsContainer.appendChild(overlay);
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
                if (this.draggedSessionId) {
                    this.handleSplitDrop(e);
                }
            });
        });

        activeGroup.sessionIds.forEach(sessionId => {
            const container = this.getSessionContainer(sessionId);
            if (container) {
                container.classList.add('drop-target');
            }
        });

        overlay.style.display = 'block';
    }

    generateDropZones(activeGroup) {
        const count = activeGroup.sessionIds.length;
        const ratio = activeGroup.splitRatio || [0.5, 0.5];

        // Get label for a pane position (using cellMapping to find the session)
        const cm = activeGroup.cellMapping || activeGroup.sessionIds.map((_, i) => i);
        const getLabel = (paneIdx) => {
            const sessionIdx = cm[paneIdx];
            const sessionId = activeGroup.sessionIds[sessionIdx];
            const session = this.sessions.get(sessionId);
            return session ? session.name : `Terminal ${paneIdx + 1}`;
        };

        if (count === 0) {
            return `<div class="drop-zone drop-full" data-position="center" data-index="0" role="button" aria-label="Drop here to create first terminal">Drop here</div>`;
        }

        if (count === 1) {
            return `
                <div class="drop-zone drop-edge drop-top" data-position="top" data-index="0" role="button" aria-label="Drop above ${this.escapeHtml(getLabel(0))}">Above "${getLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-bottom" data-position="bottom" data-index="1" role="button" aria-label="Drop below ${this.escapeHtml(getLabel(0))}">Below "${getLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-left" data-position="left" data-index="0" role="button" aria-label="Drop left of ${this.escapeHtml(getLabel(0))}">Left of "${getLabel(0)}"</div>
                <div class="drop-zone drop-edge drop-right" data-position="right" data-index="1" role="button" aria-label="Drop right of ${this.escapeHtml(getLabel(0))}">Right of "${getLabel(0)}"</div>
            `;
        }

        if (count === 2) {
            const layout = activeGroup.layout;
            const r = ratio[0];
            // Show drop zones based on current layout
            // New pane is always small, splitting the hovered terminal
            // The OTHER terminal becomes the wide pane
            if (layout === 'horizontal') {
                // Two panes side by side [left=0, right=1]
                // Dropping on left side: right becomes wide (right-wide)
                // Dropping on right side: left becomes wide (left-wide)
                const leftPct = r * 100;
                return `
                    <div class="drop-zone drop-half" style="left: 0; top: 0; width: ${leftPct}%; height: 50%;" data-position="split-above-0" data-split-target="0" role="button" aria-label="Drop above ${this.escapeHtml(getLabel(0))}">Above "${getLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 0; top: 50%; width: ${leftPct}%; height: 50%;" data-position="split-below-0" data-split-target="0" role="button" aria-label="Drop below ${this.escapeHtml(getLabel(0))}">Below "${getLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: ${leftPct}%; top: 0; width: ${100 - leftPct}%; height: 50%;" data-position="split-above-1" data-split-target="1" role="button" aria-label="Drop above ${this.escapeHtml(getLabel(1))}">Above "${getLabel(1)}"</div>
                    <div class="drop-zone drop-half" style="left: ${leftPct}%; top: 50%; width: ${100 - leftPct}%; height: 50%;" data-position="split-below-1" data-split-target="1" role="button" aria-label="Drop below ${this.escapeHtml(getLabel(1))}">Below "${getLabel(1)}"</div>
                `;
            } else {
                // Two panes stacked [top=0, bottom=1]
                // Dropping on top side: bottom becomes wide (bottom-wide)
                // Dropping on bottom side: top becomes wide (top-wide)
                const topPct = r * 100;
                return `
                    <div class="drop-zone drop-half" style="left: 0; top: 0; width: 50%; height: ${topPct}%;" data-position="split-left-0" data-split-target="0" role="button" aria-label="Drop left of ${this.escapeHtml(getLabel(0))}">Left of "${getLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 50%; top: 0; width: 50%; height: ${topPct}%;" data-position="split-right-0" data-split-target="0" role="button" aria-label="Drop right of ${this.escapeHtml(getLabel(0))}">Right of "${getLabel(0)}"</div>
                    <div class="drop-zone drop-half" style="left: 0; top: ${topPct}%; width: 50%; height: ${100 - topPct}%;" data-position="split-left-1" data-split-target="1" role="button" aria-label="Drop left of ${this.escapeHtml(getLabel(1))}">Left of "${getLabel(1)}"</div>
                    <div class="drop-zone drop-half" style="left: 50%; top: ${topPct}%; width: 50%; height: ${100 - topPct}%;" data-position="split-right-1" data-split-target="1" role="button" aria-label="Drop right of ${this.escapeHtml(getLabel(1))}">Right of "${getLabel(1)}"</div>
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

        document.querySelectorAll('.terminal-container.drop-target').forEach(el => {
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
        if (this.draggedSessionId) {
            for (const [gid, g] of this.groups) {
                if (g.sessionIds.includes(this.draggedSessionId)) {
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
        this.saveUIState();
    }

    rerenderSidebarOrder() {
        const sessionList = this.sessionList;

        for (const groupId of this.groupOrder) {
            const container = document.getElementById(`group-${groupId}`);
            if (container) {
                sessionList.appendChild(container);
            }
        }
    }

    handleSplitDrop(e) {
        const dropZone = e.target.closest('.drop-zone');
        if (!dropZone) return;

        const position = dropZone.dataset.position;
        const dropIndex = parseInt(dropZone.dataset.index) || 0;
        const activeGroup = this.groups.get(this.activeGroupId);
        if (!activeGroup || activeGroup.sessionIds.length >= 4) return;

        const draggedSessionId = this.draggedSessionId;
        if (!draggedSessionId || activeGroup.sessionIds.includes(draggedSessionId)) return;

        for (const [gid, g] of this.groups) {
            if (this.removeSessionFromGroup(g, draggedSessionId)) {
                if (g.sessionIds.length === 0) {
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

        const currentCount = activeGroup.sessionIds.length;

        if (currentCount === 0 || position === 'center') {
            activeGroup.sessionIds.push(draggedSessionId);
            activeGroup.layout = 'single';
            activeGroup.splitRatio = null;
        } else if (currentCount === 1) {
            // Always append to maintain insertion order
            activeGroup.sessionIds.push(draggedSessionId);
            activeGroup.layout = (position === 'left' || position === 'right') ? 'horizontal' : 'vertical';
            activeGroup.splitRatio = [0.5];
            // Set cellMapping based on drop position
            if (position === 'left' || position === 'top') {
                // New session (index 1) goes in pane 0, original (index 0) goes in pane 1
                activeGroup.cellMapping = [1, 0];
            } else {
                // Original (index 0) in pane 0, new (index 1) in pane 1
                activeGroup.cellMapping = [0, 1];
            }
        } else if (currentCount === 2) {
            // Handle 2->3 pane transition
            // New pane is always small, other terminal becomes wide
            // sessionIds stays in insertion order, cellMapping determines visual positions
            const currentLayout = activeGroup.layout;
            const cm = activeGroup.cellMapping || [0, 1]; // current cell mapping

            // New session is always appended (index 2)
            activeGroup.sessionIds.push(draggedSessionId);
            const newIdx = 2;

            if (position.startsWith('split-')) {
                // Parse position: split-{above|below|left|right}-{targetIdx}
                const parts = position.split('-');
                const splitDir = parts[1]; // above, below, left, right
                const targetPaneIdx = parseInt(parts[2]); // which pane to split (0 or 1)
                const targetSessionIdx = cm[targetPaneIdx]; // session index in that pane
                const otherSessionIdx = cm[1 - targetPaneIdx]; // the other session

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
                            activeGroup.cellMapping = [newIdx, otherSessionIdx, targetSessionIdx];
                        } else {
                            // target on top-left, new on bottom-left, other stays wide-right
                            activeGroup.cellMapping = [targetSessionIdx, otherSessionIdx, newIdx];
                        }
                        activeGroup.expandedQuadrant = 'right';
                    } else {
                        // Splitting right pane, left becomes wide (left-wide)
                        if (splitDir === 'above') {
                            // other stays wide-left, new on top-right, target on bottom-right
                            activeGroup.cellMapping = [otherSessionIdx, newIdx, targetSessionIdx];
                        } else {
                            // other stays wide-left, target on top-right, new on bottom-right
                            activeGroup.cellMapping = [otherSessionIdx, targetSessionIdx, newIdx];
                        }
                        activeGroup.expandedQuadrant = 'left';
                    }
                } else {
                    // Vertical: pane0=top, pane1=bottom -> splitting creates horizontal pair on one side
                    if (targetPaneIdx === 0) {
                        // Splitting top pane, bottom becomes wide (bottom-wide)
                        if (splitDir === 'left') {
                            // new on top-left, target on top-right, other stays wide-bottom
                            activeGroup.cellMapping = [newIdx, targetSessionIdx, otherSessionIdx];
                        } else {
                            // target on top-left, new on top-right, other stays wide-bottom
                            activeGroup.cellMapping = [targetSessionIdx, newIdx, otherSessionIdx];
                        }
                        activeGroup.expandedQuadrant = 'bottom';
                    } else {
                        // Splitting bottom pane, top becomes wide (top-wide)
                        if (splitDir === 'left') {
                            // other stays wide-top, new on bottom-left, target on bottom-right
                            activeGroup.cellMapping = [otherSessionIdx, newIdx, targetSessionIdx];
                        } else {
                            // other stays wide-top, target on bottom-left, new on bottom-right
                            activeGroup.cellMapping = [otherSessionIdx, targetSessionIdx, newIdx];
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
            // sessionIds stays in insertion order, cellMapping determines visual positions

            // Append new session (always at index 3)
            activeGroup.sessionIds.push(draggedSessionId);
            const newIdx = 3;

            // Get current cell mapping (maps pane position -> session index)
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

            const wideSessionIdx = cm[widePaneIdx];
            const isDropOnWide = wideQuads.includes(targetQuad);

            // Build new 4-pane cellMapping
            let newCm = [null, null, null, null];

            if (isDropOnWide) {
                // Splitting the wide pane - new terminal goes to target, wide stays in other half
                const otherWideQuad = wideQuads.find(q => q !== targetQuad);
                const otherWidePane = quadToPane[otherWideQuad];

                newCm[targetPane] = newIdx;
                newCm[otherWidePane] = wideSessionIdx;

                // Place the two small terminals in their current visual spots
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

                // New terminal at target
                newCm[targetPane] = newIdx;
                // Wide terminal moves
                newCm[wideNewPane] = wideSessionIdx;

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

        this.updateGroupInSidebar(activeGroup);
        this.updateTerminalLayout();
        this.hideDragOverlay();
        this.draggedSessionId = null;
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
        this.logsRefreshInterval = setInterval(() => {
            if (!this.logsModal.classList.contains('hidden')) {
                this.fetchLogs();
            }
        }, 2000); // Refresh every 2 seconds
    }

    stopLogsAutoRefresh() {
        if (this.logsRefreshInterval) {
            clearInterval(this.logsRefreshInterval);
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
            this.serverInfo = info;

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
                buttons: ['C-c', 'C-d', 'C-z', 'C-\\', 'C-l', 'C-r', 'C-u', 'C-w']
            }
        };
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
            themeActions.style.display = tabName === 'keybar' ? 'none' : '';
        }
    }

    openSettingsModal() {
        // Populate inputs first, then capture snapshot for comparison
        this.populateSettingsInputs();
        this.originalSettings = JSON.stringify(this.getSettingsSnapshot());
        this.settingsDiscardPending = false;
        this.updateSettingsCloseButton();

        // Set initial theme actions visibility (UI tab is active by default)
        this.updateThemeActionsVisibility('ui');

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
    }

    getSettingsFromInputs() {
        const settings = { ui: {}, terminal: {}, keybar: {} };
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
            buttons: this.settings.keybar?.buttons || this.getDefaultSettings().keybar.buttons
        };

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
            this.renderKeybar();
            this.renderMobileKeybar();
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

        // Preview the reset
        this.previewSettings();
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
        this.settings.keybar = { buttons };
        this.populateKeybarButtons();
    }

    removeKeybarButton(index) {
        if (!this.settings.keybar) {
            this.settings.keybar = { buttons: [...this.getDefaultSettings().keybar.buttons] };
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
            this.settings.keybar = { buttons: [...this.getDefaultSettings().keybar.buttons] };
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

        // Clear existing buttons except the "more" button
        const existingBtns = mobileKeybarScroll.querySelectorAll('.mobile-keybar-btn');
        existingBtns.forEach(btn => btn.remove());

        // Add new buttons (limit to first 5 valid buttons for mobile)
        const validButtons = buttons.filter(keys => this.validateKeyCombo(keys));
        validButtons.slice(0, 5).forEach(keys => {
            const btn = document.createElement('button');
            btn.className = 'mobile-keybar-btn';
            btn.dataset.keys = keys;
            btn.title = this.formatKeyTitle(keys);
            btn.setAttribute('aria-label', this.formatKeyLabel(keys));

            const label = document.createElement('span');
            label.className = 'mobile-key-label';
            label.textContent = this.formatKeyLabel(keys);

            btn.appendChild(label);

            // Insert before the "more" button
            const moreBtn = mobileKeybarScroll.querySelector('.mobile-keybar-more');
            if (moreBtn) {
                mobileKeybarScroll.insertBefore(btn, moreBtn);
            } else {
                mobileKeybarScroll.appendChild(btn);
            }
        });

        // Re-bind event listeners for new buttons
        this.bindMobileKeybarEvents();
    }

    bindKeybarEvents() {
        if (!this.keybar) return;

        // Keybar button clicks - send keys to active session
        this.keybar.querySelectorAll('.keybar-btn').forEach(btn => {
            btn.addEventListener('mousedown', (e) => {
                e.preventDefault(); // Prevent focus change
            });
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

    // Handle keybar button action - either send keys or perform special action
    async handleKeybarAction(keys) {
        // Handle special actions
        if (keys === 'Paste') {
            await this.pasteFromClipboard();
            return;
        }

        // Regular key combo - send to active session
        this.sendKeysToActiveSession({ keys: [keys] });
    }

    // Paste server-side clipboard content to active terminal
    async pasteFromClipboard() {
        try {
            const resp = await fetch(this.url('/api/clipboard'));
            if (!resp.ok) {
                this.toastError('Failed to read clipboard');
                return;
            }
            const text = await resp.text();
            if (!text) {
                this.toastWarning('Clipboard is empty');
                return;
            }
            await this.sendKeysToActiveSession({
                sequence: [{ type: 'text', value: text }]
            });
        } catch (err) {
            console.error('[clipboard] Failed to paste:', err);
            this.toastError('Failed to read clipboard');
        }
    }

    bindMobileKeybarEvents() {
        if (!this.mobileBottomToolbar) return;

        // Mobile keybar buttons - same functionality as desktop keybar
        this.mobileBottomToolbar.querySelectorAll('.mobile-keybar-btn').forEach(btn => {
            btn.addEventListener('mousedown', (e) => {
                e.preventDefault(); // Prevent focus change
            });
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
        // Connect to SSE for scratch pad updates from CLI with exponential backoff
        let retryDelay = 1000;
        const maxRetryDelay = 30000;

        const connect = () => {
            const es = new EventSource(this.url('/api/scratch/events'));

            es.onopen = () => {
                retryDelay = 1000; // Reset on successful connection
            };

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
                // Exponential backoff reconnect
                setTimeout(connect, retryDelay);
                retryDelay = Math.min(retryDelay * 2, maxRetryDelay);
            };

            this.scratchEventSource = es;
        };

        connect();
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
        // Connect to SSE for marked files updates with exponential backoff
        let retryDelay = 1000;
        const maxRetryDelay = 30000;

        const connect = () => {
            const es = new EventSource(this.url('/api/marked/events'));

            es.onopen = () => {
                retryDelay = 1000; // Reset on successful connection
            };

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
                // Exponential backoff reconnect
                setTimeout(connect, retryDelay);
                retryDelay = Math.min(retryDelay * 2, maxRetryDelay);
            };

            this.markedEventSource = es;
        };

        connect();
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

    // Get contentWindows of all terminal iframes in the active group.
    // In split views, multiple iframes exist but only one has focus.
    // Clipboard operations are broadcast to all so the focused one can handle them.
    getAllTerminalIframes() {
        const group = this.groups.get(this.activeGroupId);
        if (!group || group.sessionIds.length === 0) return [];
        const wins = [];
        for (const sessionId of group.sessionIds) {
            const container = document.getElementById(`terminal-${sessionId}`);
            if (!container) continue;
            const iframe = container.querySelector('iframe');
            if (iframe?.contentWindow) wins.push(iframe.contentWindow);
        }
        return wins;
    }

    // Write to browser clipboard via terminal iframes.
    // Broadcasts to all iframes; the focused one will succeed.
    writeClipboardViaIframes(text) {
        const iframes = this.getAllTerminalIframes();
        for (const win of iframes) {
            win.postMessage({ type: 'clipboard-write', text: text }, '*');
        }
    }

    connectClipboardEvents() {
        // Poll for clipboard changes and write to browser clipboard via iframes.
        // Polling is used instead of SSE because reverse proxies buffer SSE events.
        this.startClipboardPolling();
    }

    // Poll /api/clipboard/version to detect clipboard changes.
    // When the version changes, fetch the full content and write to system clipboard.
    // This bypasses reverse proxy SSE buffering since each poll is a complete HTTP request.
    startClipboardPolling() {
        let knownVersion = -1;

        const poll = async () => {
            try {
                const resp = await fetch(this.url('/api/clipboard/version'));
                if (!resp.ok) return;
                const version = parseInt(await resp.text(), 10);
                if (version !== knownVersion) {
                    if (knownVersion !== -1) {
                        // Version changed -- fetch the new content and write to system clipboard
                        const contentResp = await fetch(this.url('/api/clipboard'));
                        if (contentResp.ok) {
                            const text = await contentResp.text();
                            this.writeClipboardViaIframes(text);
                        }
                    }
                    knownVersion = version;
                }
            } catch (err) {
                // Fetch failed (server down, network issue) -- ignore, will retry
            }
        };

        // Poll every 300ms for low latency
        setInterval(poll, 300);
        // Initial poll immediately
        poll();
    }

}

// Initialize app
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TerminalMultiplexer();

    // Dev mode live reload - only attempt if endpoint exists
    // Uses HEAD request to probe; avoids WebSocket errors in production
    fetch(window.app.url('/api/dev-reload'), { method: 'HEAD' })
        .then(response => {
            // 400 = endpoint exists but needs WebSocket upgrade (expected)
            if (response.status === 400 || response.ok) {
                const ws = new WebSocket(window.app.wsUrl('/api/dev-reload'));
                ws.onmessage = (e) => {
                    if (e.data === 'reload') {
                        console.log('[dev] Reloading...');
                        location.reload();
                    }
                };
                ws.onerror = () => {};
            }
        })
        .catch(() => {});
});
