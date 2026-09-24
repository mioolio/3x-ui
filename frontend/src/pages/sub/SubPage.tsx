import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Card, ConfigProvider, Layout, Tabs, Tag, message } from 'antd';
import type { TabsProps } from 'antd';
import {
  AppstoreOutlined,
  ClockCircleOutlined,
  CustomerServiceOutlined,
  LinkOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons';

import { ClipboardManager, LanguageManager } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';
import { useTheme } from '@/hooks/useTheme';
import SubAppsTab from './SubAppsTab';
import SubConfigsTab from './SubConfigsTab';
import SubHeader from './SubHeader';
import SubHero from './SubHero';
import SubLinksTab from './SubLinksTab';
import SubWindowCard from './SubWindowCard';
import {
  buildSubApps,
  daysUntil,
  detectPlatform,
  hasCrossedPageBoundary,
  resolvePublicSubStatus,
  resolveSubStatus,
} from './subPageModel';
import './SubPage.css';

const subData = window.__SUB_PAGE_DATA__ || {};

const sId = subData.sId || '';
const subUrl = subData.subUrl || '';
const subJsonUrl = subData.subJsonUrl || '';
const subClashUrl = subData.subClashUrl || '';
const subTitle = subData.subTitle || '';
const subSupportUrl = subData.subSupportUrl || '';
const updateHours = Number(subData.subUpdates || 0);
const announce = subData.announce || '';
const links: string[] = Array.isArray(subData.links) ? subData.links : [];

const apps = buildSubApps({ subUrl, sId, subTitle });
const initialPlatform = detectPlatform(navigator.userAgent);
const RTL_LANGUAGES = new Set(['fa-IR', 'ar-EG']);

// The share page has its own violet accent, separate from the admin panel.
const ACCENT = {
  light: {
    primary: '#7c3aed',
    hover: '#8b5cf6',
    active: '#6d28d9',
    rail: 'rgba(124, 58, 237, 0.16)',
  },
  dark: {
    primary: '#a78bfa',
    hover: '#c4b5fd',
    active: '#8b5cf6',
    rail: 'rgba(167, 139, 250, 0.18)',
  },
};

export default function SubPage() {
  const { t } = useTranslation();
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  const [messageApi, messageContextHolder] = message.useMessage();
  useEffect(() => {
    setMessageInstance(messageApi);
  }, [messageApi]);
  const [lang, setLang] = useState<string>(() => LanguageManager.getLanguage('subscription'));
  const [now, setNow] = useState(() => Date.now());
  const [snapshotAt, setSnapshotAt] = useState(() => Date.now());
  const [liveData, setLiveData] = useState<SubPageData>(subData);
  const infoPending = useRef(false);

  const windowQuota = liveData.windowQuota;
  const accountWindows = useMemo(
    () =>
      Array.isArray(liveData.windowQuotas)
        ? liveData.windowQuotas
        : windowQuota
          ? [{ accountIndex: 1, window: windowQuota }]
          : [],
    [liveData.windowQuotas, windowQuota],
  );
  const nodes = useMemo(
    () => (Array.isArray(liveData.nodes) ? liveData.nodes : []),
    [liveData.nodes],
  );
  const accountStates = useMemo(
    () => (Array.isArray(liveData.accountStates) ? liveData.accountStates : []),
    [liveData.accountStates],
  );
  const publicState = liveData.publicState;
  const totalByte = Number(liveData.totalByte || 0);
  const usedByte =
    liveData.usedByte == null
      ? Number(liveData.downloadByte || 0) + Number(liveData.uploadByte || 0)
      : Number(liveData.usedByte);
  const expireMs = Number(liveData.expire || 0) * 1000;
  const showAccountNames = accountStates.length > 1 || accountWindows.length > 1;

  const refreshInfo = useCallback(async () => {
    if (infoPending.current) return;
    infoPending.current = true;
    try {
      const infoUrl = new URL(window.location.href);
      infoUrl.searchParams.delete('html');
      infoUrl.searchParams.delete('view');
      infoUrl.searchParams.set('format', 'info');
      const response = await fetch(infoUrl, { cache: 'no-store', credentials: 'same-origin' });
      if (response.status === 404) {
        setLiveData((previous) => ({ ...previous, enabled: false, publicState: 'blocked' }));
        return;
      }
      if (!response.ok) return;
      const info = (await response.json()) as SubPageData;
      if (info.sId !== sId) return;
      // The public info endpoint excludes credential links; keep those from
      // the original page while refreshing only subscriber-facing status.
      setLiveData((previous) => ({ ...previous, ...info }));
    } catch {
      // The next poll retries after a transient network failure.
    } finally {
      infoPending.current = false;
      setSnapshotAt(Date.now());
      setNow(Date.now());
    }
  }, []);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    const pageWindows = [
      ...accountWindows.map((entry) => entry.window),
      ...nodes.flatMap((node) => (node.window ? [node.window] : [])),
    ];
    const boundaries = [
      ...(publicState === 'active' && expireMs > snapshotAt ? [expireMs] : []),
      ...pageWindows.map((entry) => entry.resetAt).filter((resetAt) => resetAt > snapshotAt),
      ...accountStates.map((entry) => entry.expiryMs).filter((expiryMs) => expiryMs > snapshotAt),
    ];
    if (boundaries.length === 0) return;
    const nextBoundary = Math.min(...boundaries);
    const delay = Math.max(0, Math.min(nextBoundary - Date.now() + 50, 2_147_483_647));
    const timer = window.setTimeout(() => {
      const current = Date.now();
      setNow(current);
      if (
        hasCrossedPageBoundary(
          snapshotAt,
          current,
          expireMs,
          publicState,
          pageWindows,
          accountStates.map((entry) => entry.expiryMs),
        )
      ) {
        void refreshInfo();
      }
    }, delay);
    return () => window.clearTimeout(timer);
  }, [accountStates, accountWindows, expireMs, nodes, publicState, refreshInfo, snapshotAt]);

  useEffect(() => {
    // Refresh the public snapshot while the page stays open. This catches a
    // grace period ending without exposing its configured duration, and keeps
    // node-window usage current to the minute.
    const timer = window.setInterval(() => void refreshInfo(), 60_000);
    return () => window.clearInterval(timer);
  }, [refreshInfo]);

  const baseStatus = resolveSubStatus(
    { enabled: !!liveData.enabled, usedByte, totalByte, expireMs },
    now,
  );
  const displayStatus = resolvePublicSubStatus(baseStatus, publicState);
  const heroData = {
    status: displayStatus,
    daysLeft: daysUntil(expireMs, now),
    usedByte,
    totalByte,
    expireMs,
    lastOnlineMs: Number(liveData.lastOnline || 0),
    download: liveData.download || '0',
    upload: liveData.upload || '0',
    used: liveData.used || '0',
    total: liveData.total || '∞',
    remained: liveData.remained || '',
    datepicker: liveData.datepicker || 'gregorian',
  };

  const onLangChange = useCallback((next: string) => {
    setLang(next);
    LanguageManager.setLanguage(next, 'subscription');
  }, []);

  const copy = useCallback(
    async (value: string, toast?: string) => {
      if (!value) return;
      const ok = await ClipboardManager.copyText(value);
      if (ok) messageApi.success(toast ?? t('copied'));
    },
    [t, messageApi],
  );

  const open = useCallback((url: string) => {
    if (url) window.open(url, '_blank');
  }, []);

  const tabs = useMemo(() => {
    const items: NonNullable<TabsProps['items']> = [];
    if (subUrl || subJsonUrl || subClashUrl) {
      items.push({
        key: 'subscription',
        icon: <LinkOutlined />,
        label: t('subscription.tabLinks'),
        children: (
          <SubLinksTab
            subUrl={subUrl}
            subJsonUrl={subJsonUrl}
            subClashUrl={subClashUrl}
            onCopy={copy}
          />
        ),
      });
    }
    if (subUrl) {
      items.push({
        key: 'apps',
        icon: <AppstoreOutlined />,
        label: t('subscription.tabApps'),
        children: <SubAppsTab apps={apps} initialPlatform={initialPlatform} onOpen={open} />,
      });
    }
    if (links.length > 0) {
      items.push({
        key: 'configs',
        icon: <UnorderedListOutlined />,
        label: (
          <>
            {t('subscription.tabConfigs')}
            <span className="sub-tab-count">{links.length}</span>
          </>
        ),
        children: <SubConfigsTab links={links} nodes={nodes} lang={lang} now={now} onCopy={copy} />,
      });
    }
    return items;
  }, [t, copy, open, lang, now, nodes]);

  const direction = RTL_LANGUAGES.has(lang) ? 'rtl' : 'ltr';
  const pageClass = ['subscription-page', isDark && 'is-dark', isUltra && 'is-ultra']
    .filter(Boolean)
    .join(' ');

  const themeConfig = useMemo(() => {
    const accent = isDark ? ACCENT.dark : ACCENT.light;
    const primary = {
      colorPrimary: accent.primary,
      colorPrimaryHover: accent.hover,
      colorPrimaryActive: accent.active,
    };
    return {
      ...antdThemeConfig,
      token: {
        ...antdThemeConfig.token,
        ...primary,
        colorLink: accent.primary,
        colorInfo: accent.primary,
      },
      components: {
        ...antdThemeConfig.components,
        Button: { ...antdThemeConfig.components?.Button, ...primary },
        Progress: { ...antdThemeConfig.components?.Progress, remainingColor: accent.rail },
      },
    };
  }, [antdThemeConfig, isDark]);

  return (
    <ConfigProvider theme={themeConfig} direction={direction}>
      {messageContextHolder}
      <Layout className={pageClass} dir={direction}>
        <div className="sub-aurora" aria-hidden="true">
          <span className="sub-aurora-grid" />
        </div>
        <Layout.Content className="sub-content">
          <Card className="sub-card">
            <SubHeader title={subTitle} lang={lang} onLangChange={onLangChange} />
            {announce && <Alert type="info" showIcon title={announce} className="sub-announce" />}
            <SubHero {...heroData} lang={lang} />
            {accountStates.length > 1 && (
              <section
                className="sub-account-states"
                aria-label={t('subscription.accountAvailability')}
              >
                <div className="sub-section-heading">
                  <h2>{t('subscription.accountAvailability')}</h2>
                </div>
                <div className="sub-account-state-list">
                  {accountStates.map((account) => {
                    const checking =
                      account.state === 'active' && account.expiryMs > 0 && now >= account.expiryMs;
                    const label = checking
                      ? t('subscription.updatingStatus')
                      : account.state === 'grace'
                        ? t('subscription.availableAfterExpiry')
                        : account.state === 'blocked'
                          ? t('subscription.unavailable')
                          : t('subscription.active');
                    return (
                      <div className="sub-account-state" key={account.accountIndex}>
                        <span>
                          {t('subscription.accountNumber', { number: account.accountIndex })}
                        </span>
                        <Tag
                          color={checking ? 'gold' : account.state === 'blocked' ? 'red' : 'green'}
                        >
                          {label}
                        </Tag>
                      </div>
                    );
                  })}
                </div>
              </section>
            )}
            {accountWindows.some((entry) => entry.window.quotaBytes > 0) && (
              <section className="sub-window-quotas" aria-label={t('subscription.windowQuota')}>
                <div className="sub-section-heading">
                  <h2>{t('subscription.windowQuota')}</h2>
                  <span>{t('subscription.windowQuotaHint')}</span>
                </div>
                <div className="sub-account-windows">
                  {accountWindows
                    .filter((entry) => entry.window.quotaBytes > 0)
                    .map((entry) => (
                      <div className="sub-account-window" key={entry.accountIndex}>
                        {showAccountNames && (
                          <span className="sub-account-window-name">
                            {t('subscription.accountNumber', { number: entry.accountIndex })}
                          </span>
                        )}
                        <SubWindowCard window={entry.window} lang={lang} now={now} />
                      </div>
                    ))}
                </div>
              </section>
            )}
            {tabs.length > 0 && <Tabs className="sub-tabs" tabBarGutter={24} items={tabs} />}
            {(updateHours > 0 || subSupportUrl) && (
              <footer className="sub-footer">
                {updateHours > 0 && (
                  <span>
                    <ClockCircleOutlined />
                    {t('subscription.updateInterval', { hours: updateHours })}
                  </span>
                )}
                {subSupportUrl && (
                  <a href={subSupportUrl} target="_blank" rel="noopener noreferrer">
                    <CustomerServiceOutlined />
                    {t('subscription.support')}
                  </a>
                )}
              </footer>
            )}
          </Card>
        </Layout.Content>
      </Layout>
    </ConfigProvider>
  );
}
