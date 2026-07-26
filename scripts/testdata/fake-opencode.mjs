#!/usr/bin/env node

import http from 'node:http';
import process from 'node:process';

if (process.argv.includes('--version')) {
    console.log('1.18.4');
    process.exit(0);
}

const portIndex = process.argv.indexOf('--port');
const port = Number(process.argv[portIndex + 1]);
const questions = new Map();
const permissions = new Map();
const eventClients = new Set();

function json(response, value, status = 200) {
    response.writeHead(status, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify(value));
}

function emit(type, properties, directory = '/tmp/webmux-attention') {
    const data = JSON.stringify({ directory, payload: { type, properties } });
    for (const client of eventClients) client.write(`data: ${data}\n\n`);
}

function requestID(pathname, source) {
    return pathname.match(new RegExp(`/${source}/([^/]+)/(?:reply|reject)$`))?.[1] || '';
}

const appScript = `
window.__fakeOpenCodeEvents = new EventSource('/global/event');
window.__fakeOpenCodeReady = Promise.all([
  fetch('/question?directory=' + encodeURIComponent('/tmp/webmux-attention')).then(response => response.json()),
  fetch('/permission?directory=' + encodeURIComponent('/tmp/webmux-attention')).then(response => response.json()),
  new Promise(resolve => window.__fakeOpenCodeEvents.addEventListener('open', resolve, { once: true })),
]);
`;

const server = http.createServer((request, response) => {
    const url = new URL(request.url, `http://127.0.0.1:${port}`);
    if (request.method === 'GET' && (url.pathname === '/' || url.pathname.endsWith('/session/test-session'))) {
        response.writeHead(200, { 'Content-Type': 'text/html' });
        response.end('<!doctype html><html><head><script src="/assets/app.js"></script></head><body><div id="root"></div></body></html>');
        return;
    }
    if (request.method === 'GET' && url.pathname === '/assets/app.js') {
        response.writeHead(200, { 'Content-Type': 'application/javascript' });
        response.end(appScript);
        return;
    }
    if (request.method === 'GET' && url.pathname === '/global/event') {
        response.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-cache',
            Connection: 'keep-alive',
        });
        response.write(': connected\n\n');
        eventClients.add(response);
        request.on('close', () => eventClients.delete(response));
        return;
    }
    if (request.method === 'GET' && url.pathname === '/question') {
        json(response, Array.from(questions.values()));
        return;
    }
    if (request.method === 'GET' && url.pathname === '/permission') {
        json(response, Array.from(permissions.values()));
        return;
    }
    if (request.method === 'POST' && url.pathname === '/test/question/ask') {
        const item = { id: url.searchParams.get('id'), sessionID: url.searchParams.get('session') || 'test-session', questions: [] };
        questions.set(item.id, item);
        emit('question.asked', item);
        json(response, item);
        return;
    }
    if (request.method === 'POST' && url.pathname === '/test/permission/ask') {
        const item = { id: url.searchParams.get('id'), sessionID: url.searchParams.get('session') || 'test-session', permission: 'edit' };
        permissions.set(item.id, item);
        emit('permission.asked', item);
        json(response, item);
        return;
    }

    const questionID = requestID(url.pathname, 'question');
    if (request.method === 'POST' && questionID) {
        const item = questions.get(questionID);
        questions.delete(questionID);
        emit(url.pathname.endsWith('/reject') ? 'question.rejected' : 'question.replied', {
            sessionID: item?.sessionID || 'test-session', requestID: questionID,
        });
        json(response, true);
        return;
    }
    const permissionID = requestID(url.pathname, 'permission');
    if (request.method === 'POST' && permissionID) {
        const item = permissions.get(permissionID);
        permissions.delete(permissionID);
        emit(url.pathname.endsWith('/reject') ? 'permission.rejected' : 'permission.replied', {
            sessionID: item?.sessionID || 'test-session', permissionID,
        });
        json(response, true);
        return;
    }
    response.writeHead(404);
    response.end('not found');
});

server.listen(port, '127.0.0.1');
for (const signal of ['SIGINT', 'SIGTERM', 'SIGQUIT']) {
    process.on(signal, () => server.close(() => process.exit(0)));
}
