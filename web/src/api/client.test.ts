import { describe, expect, it } from 'vitest';
import { ApiError, rejectIfNotOk, unwrap } from './client.ts';

describe('ApiError', () => {
  it('uses the body\'s "detail" field as the message when present', () => {
    const err = new ApiError(401, { detail: 'invalid credentials' });
    expect(err.message).toBe('invalid credentials');
    expect(err.status).toBe(401);
    expect(err.name).toBe('ApiError');
  });

  it('falls back to the body\'s "title" field when there is no "detail"', () => {
    const err = new ApiError(422, { title: 'Unprocessable Entity' });
    expect(err.message).toBe('Unprocessable Entity');
  });

  it('falls back to a generic message when the body has neither field', () => {
    const err = new ApiError(500, undefined);
    expect(err.message).toBe('request failed with status 500');
  });
});

describe('rejectIfNotOk', () => {
  it('returns the response unchanged when it is ok', async () => {
    const response = new Response('{}', { status: 200 });
    await expect(rejectIfNotOk(response)).resolves.toBe(response);
  });

  it('throws an ApiError carrying the parsed JSON body for a non-2xx response', async () => {
    const response = new Response(JSON.stringify({ detail: 'login required' }), { status: 401 });

    await expect(rejectIfNotOk(response)).rejects.toMatchObject({
      name: 'ApiError',
      status: 401,
      message: 'login required',
    });
  });

  it('still throws an ApiError when the error body is not valid JSON', async () => {
    const response = new Response('not json', { status: 502 });

    await expect(rejectIfNotOk(response)).rejects.toMatchObject({
      name: 'ApiError',
      status: 502,
      message: 'request failed with status 502',
    });
  });
});

describe('unwrap', () => {
  it('resolves with the typed data field', async () => {
    const result = await unwrap(Promise.resolve({ data: { status: 'ok' } }));
    expect(result).toEqual({ status: 'ok' });
  });
});
