import { Progress, theme } from 'antd';
import { useTranslation } from 'react-i18next';

import { formatQuotaBytes, minutesUntil, usagePercent } from './subPageModel';

interface Props {
  window: SubWindowStatus;
  lang: string;
  now: number;
  compact?: boolean;
}

export default function SubWindowCard({ window, lang, now, compact = false }: Props) {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  const percent = usagePercent(window.usedBytes, window.quotaBytes);
  const exhausted =
    window.quotaBytes > 0 && (window.remainingBytes <= 0 || window.usedBytes >= window.quotaBytes);
  const minutes = minutesUntil(window.resetAt, now);
  const remaining = formatQuotaBytes(window.remainingBytes, lang);
  const quota = formatQuotaBytes(window.quotaBytes, lang);

  const timing =
    window.windowMode === 'fixed'
      ? minutes === null
        ? t('subscription.windowFixed')
        : minutes === 0
          ? t('subscription.windowResetDue')
          : t('subscription.windowResetsIn', { minutes })
      : minutes === null
        ? t('subscription.windowRolling')
        : minutes === 0
          ? t('subscription.windowRollingDue')
          : t('subscription.windowRollingNext', { minutes });

  return (
    <div
      className={['sub-window-card', compact && 'is-compact', exhausted && 'is-exhausted']
        .filter(Boolean)
        .join(' ')}
    >
      <div className="sub-window-ring" role="img" aria-label={`${percent.toFixed(1)}%`}>
        <Progress
          type="circle"
          percent={percent}
          status="normal"
          size={compact ? 76 : 94}
          strokeWidth={8}
          strokeColor={exhausted ? token.colorError : token.colorPrimary}
          format={() => <span className="sub-window-percent">{percent.toFixed(0)}%</span>}
        />
      </div>
      <div className="sub-window-copy">
        <span className="sub-window-eyebrow">{t('subscription.windowRemaining')}</span>
        <strong className="sub-window-balance">{remaining}</strong>
        <span className="sub-window-detail">
          {t('subscription.windowUsed')}: {formatQuotaBytes(window.usedBytes, lang)} / {quota}
        </span>
        <span className="sub-window-timing">
          {window.windowMode === 'fixed'
            ? t('subscription.windowFixed')
            : t('subscription.windowRolling')}{' '}
          · {window.windowHours}h · {timing}
        </span>
      </div>
    </div>
  );
}
