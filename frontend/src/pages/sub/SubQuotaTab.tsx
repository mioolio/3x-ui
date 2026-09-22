import { useTranslation } from 'react-i18next';
import { Progress, Tag } from 'antd';

import { SizeFormatter } from '@/utils';

export interface QuotaInfo {
  windowQuota?: number;
  windowUsed?: number;
  windowEnd?: number;
  windowMinutes?: number;
  planPeriod?: string;
  planQuota?: number;
  periodUsed?: number;
  planEnd?: number;
  throttledSince?: number;
  totalQuota?: number;
  used?: number;
  historyUsed?: number;
  expiry?: number;
  speedUp?: number;
  speedDown?: number;
}

// Rates are stored in Kbps; display Mbps once the value is a whole multiple.
function formatSpeed(kbps?: number): string {
  const v = Number(kbps) || 0;
  if (v <= 0) return '∞';
  if (v >= 1000) {
    const mbps = v / 1000;
    return `${Number.isInteger(mbps) ? mbps : mbps.toFixed(1)} Mbps`;
  }
  return `${v} Kbps`;
}

interface SubQuotaTabProps {
  quota: QuotaInfo;
  usedLabel: string;
  totalLabel: string;
}

function pct(used: number, quota: number): number {
  if (quota <= 0) return 0;
  return Math.min(100, Math.round((used / quota) * 1000) / 10);
}

function resetLabel(endSec: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  if (!endSec) return '—';
  const secs = Math.max(0, endSec - Math.floor(Date.now() / 1000));
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  if (h >= 24) {
    const days = Math.floor(h / 24);
    return t('subscription.resetsInDays', { days, hours: h % 24 });
  }
  return t('subscription.resetsIn', { hours: h, minutes: m });
}

export default function SubQuotaTab({ quota, usedLabel, totalLabel }: SubQuotaTabProps) {
  const { t } = useTranslation();
  const totalQuota = Number(quota.totalQuota) || 0;
  const used = Number(quota.used) || 0;
  const historyUsed = Number(quota.historyUsed) || 0;
  const throttled = Number(quota.throttledSince) > 0;

  const rows: {
    key: string;
    label: string;
    present: boolean;
    used: number;
    quotaB: number;
    end: number;
    period?: string;
  }[] = [
    {
      key: 'total',
      label: t('subscription.totalQuota'),
      present: true,
      used,
      quotaB: totalQuota,
      end: 0,
    },
    {
      key: 'plan',
      label: t('subscription.planQuota'),
      present: !!quota.planPeriod && (Number(quota.planQuota) || 0) > 0,
      used: Number(quota.periodUsed) || 0,
      quotaB: Number(quota.planQuota) || 0,
      end: Number(quota.planEnd) || 0,
      period: quota.planPeriod,
    },
    {
      key: 'window',
      label: t('subscription.windowQuota'),
      present: (Number(quota.windowMinutes) || 0) > 0 && (Number(quota.windowQuota) || 0) > 0,
      used: Number(quota.windowUsed) || 0,
      quotaB: Number(quota.windowQuota) || 0,
      end: Number(quota.windowEnd) || 0,
    },
  ];

  return (
    <div className="sub-rows">
      {throttled && (
        <div className="sub-configs-bar">
          <Tag color="orange">{t('subscription.throttledNow')}</Tag>
        </div>
      )}
      {rows
        .filter((r) => r.present)
        .map((row) => (
          <div key={row.key} className="sub-row">
            <div className="sub-row-info" style={{ width: '100%' }}>
              <div
                className="sub-label"
                style={{ display: 'flex', justifyContent: 'space-between' }}
              >
                <span>
                  {row.label}
                  {row.period && (
                    <Tag color="purple" style={{ marginInlineStart: 8 }}>
                      {t(`subscription.period.${row.period}`)}
                    </Tag>
                  )}
                </span>
                <span>
                  {SizeFormatter.sizeFormat(row.used)}
                  {' / '}
                  {row.quotaB > 0 ? SizeFormatter.sizeFormat(row.quotaB) : totalLabel}
                </span>
              </div>
              <Progress
                percent={row.quotaB > 0 ? pct(row.used, row.quotaB) : 0}
                status={row.quotaB > 0 && row.used >= row.quotaB ? 'exception' : 'normal'}
                size={['100%', 12]}
              />
              {row.end > 0 && (
                <div className="sub-label">
                  {t('subscription.resets')} {resetLabel(row.end, t)}
                </div>
              )}
            </div>
          </div>
        ))}
      <div className="sub-row-info" style={{ width: '100%' }}>
        <div className="sub-label" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>{t('subscription.speedLimit')}</span>
          <span>
            ↓ {formatSpeed(quota.speedDown)} / ↑ {formatSpeed(quota.speedUp)}
          </span>
        </div>
        <div className="sub-label" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>{t('subscription.historyUsage')}</span>
          <span>{SizeFormatter.sizeFormat(historyUsed)}</span>
        </div>
        <div className="sub-label" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>{t('subscription.currentUsage')}</span>
          <span>{usedLabel}</span>
        </div>
      </div>
    </div>
  );
}
