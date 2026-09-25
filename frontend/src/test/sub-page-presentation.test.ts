import { describe, expect, it } from 'vitest';

import {
  formatBilledTrafficBytes,
  formatMaximumKbps,
  formatTrafficMultiplier,
  hasCrossedPageBoundary,
  minutesUntil,
  resolvePublicSubStatus,
} from '@/pages/sub/subPageModel';

describe('subscriber display values', () => {
  it('shows directional quota debits instead of physical transfer when billed values are supplied', () => {
    expect(formatBilledTrafficBytes(31.71 * 50 * 1024 ** 2, '31.71MB')).toBe('1.55GB');
    expect(formatBilledTrafficBytes(1.46 * 50 * 1024 ** 2, '1.46MB')).toBe('73.00MB');
    expect(formatBilledTrafficBytes(0, '31.71MB')).toBe('0.00B');
    expect(formatBilledTrafficBytes(undefined, '31.71MB')).toBe('31.71MB');
    expect(formatBilledTrafficBytes('invalid', '31.71MB')).toBe('31.71MB');
  });

  it('rounds a fixed-window countdown up to the next minute and never goes negative', () => {
    const now = 1_700_000_000_000;
    expect(minutesUntil(now + 60_001, now)).toBe(2);
    expect(minutesUntil(now + 60_000, now)).toBe(1);
    expect(minutesUntil(now + 1, now)).toBe(1);
    expect(minutesUntil(now, now)).toBe(0);
    expect(minutesUntil(0, now)).toBeNull();
  });

  it('shows maximum speed and usage multiplier without presenting unlimited as zero', () => {
    expect(formatMaximumKbps(0, 'en-US')).toBe('∞');
    expect(formatMaximumKbps(750, 'en-US')).toBe('750 Kbps');
    expect(formatMaximumKbps(10_000, 'en-US')).toBe('10 Mbps');
    expect(formatTrafficMultiplier(100, 'en-US')).toBe('0.01x');
    expect(formatTrafficMultiplier(20_000, 'en-US')).toBe('2x');
    expect(formatTrafficMultiplier(500_001, 'en-US')).toBe('50.0001x');
    expect(formatTrafficMultiplier(500_001, 'de-DE')).toBe('50,0001x');
    expect(formatTrafficMultiplier(Number.MAX_SAFE_INTEGER, 'en-US')).toBe('900,719,925,474.0991x');
  });

  it('refreshes a long-open snapshot only when a deadline or window reset is crossed', () => {
    const openedAt = 1_700_000_000_000;
    const expiry = openedAt + 60_000;
    const resetAt = openedAt + 120_000;
    expect(
      hasCrossedPageBoundary(openedAt, openedAt + 30_000, expiry, 'active', [{ resetAt }]),
    ).toBe(false);
    expect(hasCrossedPageBoundary(openedAt, expiry, expiry, 'active', [])).toBe(true);
    expect(hasCrossedPageBoundary(openedAt, expiry, expiry, 'blocked', [])).toBe(false);
    expect(hasCrossedPageBoundary(openedAt, resetAt, 0, 'blocked', [{ resetAt }])).toBe(true);
    expect(
      hasCrossedPageBoundary(openedAt, resetAt, 0, 'active', [{ resetAt: openedAt - 1 }]),
    ).toBe(false);
    expect(hasCrossedPageBoundary(openedAt, expiry, 0, 'mixed', [], [expiry])).toBe(true);
  });

  it('never lets an old active snapshot present an expired account as available', () => {
    expect(resolvePublicSubStatus('expired', 'active')).toBe('expired');
    expect(resolvePublicSubStatus('depleted', 'active')).toBe('active');
    expect(resolvePublicSubStatus('active', 'mixed')).toBe('mixed');
  });
});
