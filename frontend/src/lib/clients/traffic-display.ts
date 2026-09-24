import { ColorUtils } from '@/utils';

export interface TrafficDisplayInput {
  up: number;
  down: number;
  billedUp?: number;
  billedDown?: number;
  chargeExtraBytes?: number;
  chargeDiscountBytes?: number;
  total: number;
  enabled: boolean;
  trafficDiff: number;
}

export interface TrafficDisplay {
  used: number;
  remaining: number;
  percent: number;
  isUnlimited: boolean;
  isDepleted: boolean;
  strokeColor: string;
  status: 'normal' | 'exception' | undefined;
}

const DISABLED_STROKE = {
  light: '#bcbcbc',
  dark: 'rgb(72, 84, 105)',
} as const;

const UNLIMITED_STROKE = '#722ed1';

export function chargedTrafficBytes(
  traffic: Partial<
    Pick<
      TrafficDisplayInput,
      'up' | 'down' | 'billedUp' | 'billedDown' | 'chargeExtraBytes' | 'chargeDiscountBytes'
    >
  >,
): number {
  if (Number.isFinite(traffic.billedUp) && Number.isFinite(traffic.billedDown)) {
    return Math.max(0, traffic.billedUp!) + Math.max(0, traffic.billedDown!);
  }
  return Math.max(
    0,
    (traffic.up || 0) +
      (traffic.down || 0) +
      Math.max(0, traffic.chargeExtraBytes || 0) -
      Math.max(0, traffic.chargeDiscountBytes || 0),
  );
}

// Older panel responses only carry a combined surcharge/discount. Allocate that
// total proportionally so the direction breakdown still adds up to the quota.
export function billedTrafficDirections(traffic: Partial<TrafficDisplayInput>): {
  up: number;
  down: number;
} {
  if (Number.isFinite(traffic.billedUp) && Number.isFinite(traffic.billedDown)) {
    return { up: Math.max(0, traffic.billedUp!), down: Math.max(0, traffic.billedDown!) };
  }
  const total = chargedTrafficBytes(traffic);
  const physicalUp = Math.max(0, traffic.up || 0);
  const physicalDown = Math.max(0, traffic.down || 0);
  const physicalTotal = physicalUp + physicalDown;
  if (physicalTotal <= 0) return { up: 0, down: total };
  const up = Math.round((total * physicalUp) / physicalTotal);
  return { up, down: total - up };
}

export function computeTrafficDisplay(input: TrafficDisplayInput, isDark: boolean): TrafficDisplay {
  const used = chargedTrafficBytes(input);
  const total = input.total || 0;
  const isUnlimited = total <= 0;

  let percent = 100;
  if (!isUnlimited) {
    percent = Math.min(100, Math.max(0, (used / total) * 100));
  }

  const isDepleted = !isUnlimited && used >= total;
  const remaining = isUnlimited ? 0 : Math.max(0, total - used);

  let strokeColor: string;
  if (!input.enabled) {
    strokeColor = isDark ? DISABLED_STROKE.dark : DISABLED_STROKE.light;
  } else if (isUnlimited) {
    strokeColor = UNLIMITED_STROKE;
  } else {
    strokeColor = ColorUtils.clientUsageColor({ up: used, down: 0, total }, input.trafficDiff);
  }

  return {
    used,
    remaining,
    percent,
    isUnlimited,
    isDepleted,
    strokeColor,
    status: isDepleted && input.enabled ? 'exception' : undefined,
  };
}
