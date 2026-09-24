export const MAX_TRAFFIC_MULTIPLIER_BPS = Number.MAX_SAFE_INTEGER;
export const MAX_TRAFFIC_MULTIPLIER_INPUT = '900719925474.0991';

const BASIS_POINTS_PER_MULTIPLIER = 10_000;

// Keep the UI decimal exact even near Number.MAX_SAFE_INTEGER, where
// multiplying a floating-point display value by 10,000 loses basis points.
export function trafficMultiplierBpsToInput(value: unknown): string {
  const bps =
    typeof value === 'number' && Number.isSafeInteger(value) && value >= 100
      ? value
      : BASIS_POINTS_PER_MULTIPLIER;
  const whole = Math.floor(bps / BASIS_POINTS_PER_MULTIPLIER);
  const fraction = String(bps % BASIS_POINTS_PER_MULTIPLIER)
    .padStart(4, '0')
    .replace(/0+$/, '');
  return fraction ? `${whole}.${fraction}` : String(whole);
}

export function trafficMultiplierInputToBps(value: unknown): number {
  if (value == null || value === '') return BASIS_POINTS_PER_MULTIPLIER;
  const match = /^([0-9]+)(?:\.([0-9]{1,4}))?$/.exec(String(value));
  if (!match) return Number.NaN;
  const bps = BigInt(match[1]) * 10_000n + BigInt((match[2] || '').padEnd(4, '0'));
  // A non-safe result must reach schema validation as invalid, never be
  // silently rounded into a different billable multiplier.
  if (bps > BigInt(MAX_TRAFFIC_MULTIPLIER_BPS)) return Number.MAX_SAFE_INTEGER + 1;
  return Number(bps);
}
