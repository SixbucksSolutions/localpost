import assert from 'node:assert/strict';
import {test} from 'node:test';
import {browserProof} from './qualify-adoption.mjs';

const id = '123e4567-e89b-42d3-a456-426614174000';

test('browser uses exact profile framing and sends bounded safe diagnostic', async t => {
  const requests = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    requests.push({url, options});
    if (requests.length === 1) return Response.json({XADD: '1-0'});
    if (requests.length === 2) return Response.json({XRANGE: [{id: '1-0', msg: {data: 'b64:AP8NCoA='}}]});
    return Response.json({XADD: '2-0'});
  });
  assert.deepEqual(await browserProof('http://127.0.0.1:53170', id), {
    status: 'passed', checks: ['binary-send-read', 'diagnostic-delivery'],
  });
  assert.equal(requests.length, 3);
  assert.equal(requests[0].url, `http://127.0.0.1:53170/XADD/localpost%3Av1%3A${id}/*/data`);
  assert.equal(requests[0].options.body, 'b64:AP8NCoA=');
  assert.equal(requests[1].options.body, `XRANGE/localpost%3Av1%3A${id}/-/%2B/COUNT/1`);
  for (const {options} of requests) {
    assert.equal(options.credentials, 'omit');
    assert.equal(options.redirect, 'error');
    assert.equal(options.cache, 'no-store');
  }
  assert.deepEqual(JSON.parse(Buffer.from(requests[2].options.body.slice(4), 'base64')), {
    operation: 'adoption-browser', phase: 'send-read', status: 'passed', correlation: id,
  });
});

for (const [name, response, error] of [
  ['HTTP failure', () => new Response('', {status: 403}), 'http_error'],
  ['backend OOM', () => Response.json({XADD: [false, 'OOM rejected']}), 'memory_limit'],
  ['unexpected key', () => Response.json({XADD: '1-0', extra: true}), 'invalid_response'],
  ['oversize response', () => new Response('x'.repeat(65537)), 'response_too_large'],
]) {
  test(`browser rejects ${name}`, async t => {
    t.mock.method(globalThis, 'fetch', response);
    await assert.rejects(browserProof('http://127.0.0.1:53170', id), new RegExp(error));
  });
}

test('browser validates read entry shape before diagnostic delivery', async t => {
  let calls = 0;
  t.mock.method(globalThis, 'fetch', async () => ++calls === 1
    ? Response.json({XADD: '1-0'})
    : Response.json({XRANGE: [{id: '1-0', msg: {data: 'b64:AP8NCoA=', extra: true}}]}));
  await assert.rejects(browserProof('http://127.0.0.1:53170', id), /invalid_response/);
  assert.equal(calls, 2);
});
