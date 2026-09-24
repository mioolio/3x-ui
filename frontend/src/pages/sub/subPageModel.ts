const DAY_MS = 86_400_000;

export type SubStatus =
  | 'active'
  | 'unlimited'
  | 'expired'
  | 'depleted'
  | 'disabled'
  | 'grace'
  | 'throttled'
  | 'blocked'
  | 'mixed';

export interface SubUsage {
  enabled: boolean;
  usedByte: number;
  totalByte: number;
  expireMs: number;
}

export function resolveSubStatus(sub: SubUsage, now: number): SubStatus {
  if (!sub.enabled) return 'disabled';
  if (sub.expireMs > 0 && now >= sub.expireMs) return 'expired';
  if (sub.totalByte > 0 && sub.usedByte >= sub.totalByte) return 'depleted';
  if (sub.totalByte <= 0 && sub.expireMs === 0) return 'unlimited';
  return 'active';
}

export function resolvePublicSubStatus(
  baseStatus: SubStatus,
  publicState: 'active' | 'grace' | 'blocked' | 'mixed' | undefined,
): SubStatus {
  if (baseStatus === 'disabled') return 'disabled';
  if (publicState === 'grace' || publicState === 'mixed') return publicState;
  if (publicState === 'blocked') {
    return baseStatus === 'expired' || baseStatus === 'depleted' ? baseStatus : 'blocked';
  }
  // A server-confirmed active state can keep a depleted allowance usable under
  // its configured policy. An old active snapshot cannot override a deadline.
  if (publicState === 'active' && baseStatus === 'depleted') return 'active';
  return baseStatus;
}

export function daysUntil(expireMs: number, now: number): number | null {
  if (expireMs <= 0) return null;
  return Math.max(0, Math.ceil((expireMs - now) / DAY_MS));
}

export function usagePercent(usedByte: number, totalByte: number): number {
  if (totalByte <= 0) return 0;
  const pct = (usedByte / totalByte) * 100;
  return Number.isFinite(pct) ? Math.min(100, Math.max(0, pct)) : 0;
}

export function minutesUntil(resetAt: number, now: number): number | null {
  if (!Number.isFinite(resetAt) || resetAt <= 0) return null;
  return Math.max(0, Math.ceil((resetAt - now) / 60_000));
}

// The page is a server snapshot. Once a quota window or the account deadline
// passes, reload that snapshot so the state and next reset time stay truthful.
export function hasCrossedPageBoundary(
  openedAt: number,
  now: number,
  expireMs: number,
  publicState: string | undefined,
  windows: ReadonlyArray<{ resetAt: number }>,
  accountExpiries: ReadonlyArray<number> = [],
): boolean {
  if (now <= openedAt) return false;
  if (publicState === 'active' && expireMs > openedAt && expireMs <= now) return true;
  if (accountExpiries.some((expiry) => expiry > openedAt && expiry <= now)) return true;
  return windows.some(({ resetAt }) => resetAt > openedAt && resetAt <= now);
}

export function formatQuotaBytes(bytes: number, lang: string): string {
  const safe = Math.max(0, Number.isFinite(bytes) ? bytes : 0);
  const unit = safe >= 1024 ** 3 ? 1024 ** 3 : 1024 ** 2;
  return `${new Intl.NumberFormat(lang, { maximumFractionDigits: 2 }).format(safe / unit)} ${unit === 1024 ** 3 ? 'GB' : 'MB'}`;
}

// The public API reports directional quota debits separately from physical
// transfer counters. Keep the same units as the server's traffic formatter.
export function formatBilledTrafficBytes(
  billedBytes: string | number | null | undefined,
  legacyDisplay: string | undefined,
): string {
  const fallback = legacyDisplay || '0';
  if (billedBytes == null) return fallback;
  let amount = Number(billedBytes);
  if (!Number.isFinite(amount) || amount < 0) return fallback;
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit++;
  }
  return `${amount.toFixed(2)}${units[unit]}`;
}

export function formatMaximumKbps(kbps: number, lang: string): string {
  if (!Number.isFinite(kbps) || kbps <= 0) return '∞';
  const unit = kbps >= 1000 ? 1000 : 1;
  return `${new Intl.NumberFormat(lang, { maximumFractionDigits: 2 }).format(kbps / unit)} ${unit === 1000 ? 'Mbps' : 'Kbps'}`;
}

export function formatTrafficMultiplier(bps: number, lang: string): string {
  const factor = (Number.isFinite(bps) && bps > 0 ? bps : 10_000) / 10_000;
  return `${new Intl.NumberFormat(lang, { maximumFractionDigits: 2 }).format(factor)}x`;
}

export type AppPlatform = 'android' | 'ios';

export function detectPlatform(userAgent: string): AppPlatform {
  // iPadOS sends a Macintosh UA, and App Store clients also run on Apple-silicon Macs.
  if (/iphone|ipad|ipod|macintosh/i.test(userAgent)) return 'ios';
  return 'android';
}

export interface SubApp {
  name: string;
  url: string;
}

export interface SubAppSource {
  subUrl: string;
  sId: string;
  subTitle: string;
}

export function buildSubApps({
  subUrl,
  sId,
  subTitle,
}: SubAppSource): Record<AppPlatform, SubApp[]> {
  const encSub = encodeURIComponent(subUrl);
  const profileName = encodeURIComponent(subTitle || sId);

  const v2box = {
    name: 'V2Box',
    url: `v2box://install-sub?url=${encSub}&name=${encodeURIComponent(sId)}`,
  };
  const singBox = {
    name: 'Sing-box',
    url: `sing-box://import-remote-profile?url=${encSub}#${profileName}`,
  };
  const v2raytun = { name: 'V2RayTun', url: `v2raytun://import/${subUrl}` };
  const happ = { name: 'Happ', url: `happ://add/${subUrl}` };
  const incy = { name: 'Incy', url: `incy://add/${subUrl}` };
  const rocketSource = `${subUrl}${subUrl.includes('?') ? '&' : '?'}flag=shadowrocket`;
  const rocketRemark = encodeURIComponent(subTitle || sId || 'Subscription');

  return {
    android: [
      v2box,
      { name: 'V2RayNG', url: `v2rayng://install-config?url=${encSub}` },
      singBox,
      v2raytun,
      happ,
      incy,
    ],
    ios: [
      {
        name: 'Shadowrocket',
        url: `shadowrocket://add/sub://${btoa(rocketSource)}?remark=${rocketRemark}`,
      },
      v2box,
      { name: 'Streisand', url: `streisand://import/${encSub}` },
      v2raytun,
      happ,
      incy,
    ],
  };
}
