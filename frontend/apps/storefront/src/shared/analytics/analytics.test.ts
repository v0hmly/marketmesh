import { afterEach, describe, expect, it, vi } from 'vitest';
import { createAnalytics } from './index';

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('Rybbit privacy and lifecycle', () => {
  it('sends one view per path and only allowed fields', () => {
    const send = vi.fn().mockResolvedValue(new Response());
    const analytics = createAnalytics(true, send);
    analytics.pageview('/login');
    analytics.pageview('/login');
    analytics.pageview('/account');
    analytics.pageview('/login?password=private');
    analytics.event('login_succeeded');
    expect(send).toHaveBeenCalledTimes(3);
    for (const [url, options] of send.mock.calls) {
      expect(url).toBe('/analytics/track');
      expect(options).toMatchObject({
        credentials: 'omit',
        referrerPolicy: 'no-referrer',
        redirect: 'error',
      });
      expect(Object.keys(JSON.parse(options.body)).sort()).toEqual(
        options.body.includes('event_name')
          ? ['event_name', 'pathname', 'type']
          : ['pathname', 'type'],
      );
      expect(options.body).not.toContain('private');
    }
  });
  it('does nothing when disabled or Do Not Track is set', () => {
    const send = vi.fn();
    createAnalytics(false, send).pageview('/account');
    vi.stubGlobal('navigator', { doNotTrack: '1' });
    createAnalytics(true, send).event('login_succeeded');
    expect(send).not.toHaveBeenCalled();
  });
  it('contains errors and bounds pending requests during an outage', async () => {
    const rejected = vi.fn().mockRejectedValue(new Error('offline'));
    expect(() => createAnalytics(true, rejected).event('login_succeeded')).not.toThrow();
    await Promise.resolve();
    const send = vi.fn().mockReturnValue(new Promise(() => undefined));
    const analytics = createAnalytics(true, send);
    for (let i = 0; i < 30; i++) analytics.event('login_succeeded');
    expect(send).toHaveBeenCalledTimes(8);
    expect(() =>
      createAnalytics(true, () => {
        throw new Error('blocked');
      }).pageview('/login'),
    ).not.toThrow();
  });
});
