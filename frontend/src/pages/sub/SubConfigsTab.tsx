import { Fragment } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Tag } from 'antd';
import { CopyOutlined } from '@ant-design/icons';

import ConfigBlock from '@/components/clients/ConfigBlock';
import {
  amneziawgConfigFromLink,
  isPostQuantumLink,
  wireguardConfigFromLink,
} from '@/lib/xray/inbound-link';
import { LinkTags, parseLinkParts } from '@/lib/xray/link-label';
import SubQrButton from './SubQrButton';
import SubWindowCard from './SubWindowCard';
import { formatMaximumKbps, formatTrafficMultiplier } from './subPageModel';

interface SubConfigsTabProps {
  links: string[];
  nodes: SubNodeOverview[];
  lang: string;
  now: number;
  onCopy: (value: string, toast?: string) => void;
}

export default function SubConfigsTab({ links, nodes, lang, now, onCopy }: SubConfigsTabProps) {
  const { t } = useTranslation();
  const multiAccount = new Set(nodes.map((node) => node.accountIndex)).size > 1;

  return (
    <div className="sub-rows">
      {nodes.length > 0 && (
        <section className="sub-node-overviews" aria-label={t('subscription.nodeOverview')}>
          <div className="sub-section-heading">
            <h2>{t('subscription.nodeOverview')}</h2>
            <span>{t('subscription.nodeOverviewHint')}</span>
          </div>
          <div className="sub-node-grid">
            {nodes.map((node) => (
              <article className="sub-node-card" key={`${node.inboundId}:${node.accountIndex}`}>
                <div className="sub-node-heading">
                  <div className="sub-node-name">
                    <span className="sub-node-overline">{node.protocol.toUpperCase()}</span>
                    <h3 dir="auto">{node.remark || `#${node.inboundId}`}</h3>
                    {multiAccount && (
                      <span className="sub-node-account">
                        {t('subscription.accountNumber', { number: node.accountIndex })}
                      </span>
                    )}
                  </div>
                  {(node.trafficMultiplierBps !== 10_000 || !!node.activeMultiplierBps) && (
                    <div className="sub-multiplier-tags">
                      <Tag className="sub-multiplier-tag" title={t('subscription.trafficFactor')}>
                        {formatTrafficMultiplier(node.trafficMultiplierBps, lang)}
                      </Tag>
                      {node.activeMultiplierBps &&
                        node.activeMultiplierBps > node.trafficMultiplierBps && (
                          <Tag
                            className="sub-multiplier-tag is-active-overage"
                            title={t('subscription.activeOverageFactor')}
                            aria-label={`${t('subscription.activeOverageFactor')}: ${formatTrafficMultiplier(node.activeMultiplierBps, lang)}`}
                          >
                            {formatTrafficMultiplier(node.activeMultiplierBps, lang)}
                          </Tag>
                        )}
                    </div>
                  )}
                </div>
                <dl className="sub-node-speeds">
                  <div>
                    <dt>{t('subscription.maxUpload')}</dt>
                    <dd>{formatMaximumKbps(node.maxUpKbps, lang)}</dd>
                  </div>
                  <div>
                    <dt>{t('subscription.maxDownload')}</dt>
                    <dd>{formatMaximumKbps(node.maxDownKbps, lang)}</dd>
                  </div>
                </dl>
                {node.usageTracked === false ? (
                  <div className="sub-node-shared">{t('subscription.nodeUsageNotTracked')}</div>
                ) : node.window && node.window.quotaBytes > 0 ? (
                  <SubWindowCard window={node.window} lang={lang} now={now} compact />
                ) : node.windowConfigured ? (
                  <div className="sub-node-shared">{t('subscription.nodeUsageUnavailable')}</div>
                ) : (
                  <div className="sub-node-shared">{t('subscription.sharedQuota')}</div>
                )}
              </article>
            ))}
          </div>
        </section>
      )}
      <div className="sub-configs-bar">
        <h2>{t('subscription.individualLinks')}</h2>
        <Button
          icon={<CopyOutlined />}
          onClick={() => onCopy(links.join('\n'), t('subscription.copyAllConfigsCopied'))}
        >
          {t('subscription.copyAllConfigs')}
        </Button>
      </div>
      {links.map((link, idx) => {
        const parts = parseLinkParts(link);
        const rowTitle = parts?.remark || `Link ${idx + 1}`;
        const isWireguardLink = link.startsWith('wireguard://') || link.startsWith('wg://');
        const isAmneziawgLink = link.startsWith('vpn://');
        return (
          <Fragment key={link}>
            <div className="sub-row">
              {parts ? <LinkTags parts={parts} /> : <Tag className="sub-row-tag">LINK</Tag>}
              <span className="sub-row-title" dir="auto" title={rowTitle}>
                {rowTitle}
              </span>
              <div className="sub-row-actions">
                <Button
                  icon={<CopyOutlined />}
                  onClick={() => onCopy(link)}
                  aria-label={t('copy')}
                  title={t('copy')}
                />
                {!isPostQuantumLink(link) && (
                  <SubQrButton value={link} label={rowTitle} onCopy={onCopy} />
                )}
              </div>
            </div>
            {isWireguardLink && (
              <ConfigBlock
                label={t('pages.clients.wireguardConfig')}
                text={wireguardConfigFromLink(link, rowTitle)}
                fileName={`${rowTitle || 'peer'}.conf`}
                qrRemark={rowTitle}
                tagColor="cyan"
              />
            )}
            {isAmneziawgLink && (
              <ConfigBlock
                label={t('pages.clients.amneziaWgConfig')}
                text={amneziawgConfigFromLink(link)}
                fileName={`${rowTitle || 'peer'}.conf`}
                qrRemark={rowTitle}
                tagColor="purple"
              />
            )}
          </Fragment>
        );
      })}
    </div>
  );
}
