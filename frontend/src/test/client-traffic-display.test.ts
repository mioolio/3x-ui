import { describe, it, expect } from 'vitest';

import { computeTrafficDisplay } from '@/lib/clients/traffic-display';
import { ClientTrafficSchema } from '@/schemas/client';

describe('computeTrafficDisplay', () => {
  const gb = 1024 * 1024 * 1024;

  it('returns 50% for half-used limited quota', () => {
    const d = computeTrafficDisplay(
      { up: 0.25 * gb, down: 0.25 * gb, total: gb, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.percent).toBe(50);
    expect(d.isUnlimited).toBe(false);
    expect(d.remaining).toBe(0.5 * gb);
  });

  it('returns 100% bar for unlimited clients', () => {
    const d = computeTrafficDisplay(
      { up: 5 * gb, down: 2 * gb, total: 0, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.percent).toBe(100);
    expect(d.isUnlimited).toBe(true);
    expect(d.strokeColor).toBe('#722ed1');
  });

  it('marks depleted clients with exception status', () => {
    const d = computeTrafficDisplay(
      { up: gb, down: 0, total: gb, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(d.isDepleted).toBe(true);
    expect(d.status).toBe('exception');
    expect(d.percent).toBe(100);
  });

  it('uses gray stroke when client is disabled', () => {
    const d = computeTrafficDisplay(
      { up: 0.5 * gb, down: 0, total: gb, enabled: false, trafficDiff: 0 },
      false,
    );
    expect(d.strokeColor).toBe('#bcbcbc');
    expect(d.status).toBeUndefined();
  });

  it('uses warning color near traffic limit', () => {
    const diff = 0.1 * gb;
    const d = computeTrafficDisplay(
      { up: 0.95 * gb, down: 0, total: gb, enabled: true, trafficDiff: diff },
      false,
    );
    expect(d.strokeColor).toBe('#faad14');
  });

  it('uses billed traffic for the quota while keeping physical directions in the parsed row', () => {
    const traffic = ClientTrafficSchema.parse({
      up: 0,
      down: 20 * 1024 * 1024,
      chargeExtraBytes: 980 * 1024 * 1024,
      chargeDiscountBytes: 0,
    });
    const d = computeTrafficDisplay(
      {
        ...traffic,
        up: traffic.up || 0,
        down: traffic.down || 0,
        total: gb,
        enabled: true,
        trafficDiff: 0,
      },
      false,
    );
    expect(traffic.down).toBe(20 * 1024 * 1024);
    expect(d.used).toBe(1000 * 1024 * 1024);
    expect(d.remaining).toBe(24 * 1024 * 1024);
    expect(d.percent).toBeCloseTo((1000 / 1024) * 100);
  });

  it('subtracts a discount from billed usage and never shows negative usage', () => {
    const discounted = computeTrafficDisplay(
      { up: 20, down: 0, chargeDiscountBytes: 19, total: 10, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(discounted.used).toBe(1);
    expect(discounted.remaining).toBe(9);
    expect(discounted.isDepleted).toBe(false);

    const overDiscounted = computeTrafficDisplay(
      { up: 20, down: 0, chargeDiscountBytes: 30, total: 10, enabled: true, trafficDiff: 0 },
      false,
    );
    expect(overDiscounted.used).toBe(0);
  });
});
