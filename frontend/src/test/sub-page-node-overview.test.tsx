import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import SubConfigsTab from '@/pages/sub/SubConfigsTab';

describe('subscription config overview', () => {
  it('shows node terms and per-node window progress without exposing internal policy actions', () => {
    const now = 1_700_000_000_000;
    render(
      <SubConfigsTab
        links={['vless://11111111-1111-1111-1111-111111111111@example.com:443#Example']}
        nodes={[
          {
            inboundId: 7,
            accountIndex: 1,
            remark: 'Berlin',
            protocol: 'vless',
            maxUpKbps: 6_000,
            maxDownKbps: 10_000,
            trafficMultiplierBps: 20_000,
            window: {
              quotaBytes: 10 * 1024 ** 3,
              usedBytes: 4 * 1024 ** 3,
              remainingBytes: 6 * 1024 ** 3,
              windowHours: 2,
              windowMode: 'fixed',
              windowStart: now - 3_600_000,
              resetAt: now + 3_600_000,
            },
          },
        ]}
        lang="en-US"
        now={now}
        onCopy={() => {}}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Node details' })).toBeDefined();
    expect(screen.getByRole('heading', { name: 'Berlin' })).toBeDefined();
    expect(screen.getByText('2x')).toBeDefined();
    expect(screen.getByText('6 Mbps')).toBeDefined();
    expect(screen.getByText('10 Mbps')).toBeDefined();
    expect(screen.getByText('Refreshes in 60 min', { exact: false })).toBeDefined();
    expect(screen.queryByText(/stop when quota|reduced speed|overage/i)).toBeNull();
  });

  it('distinguishes two accounts on the same node without merging their balances', () => {
    const shared = {
      inboundId: 7,
      remark: 'Berlin',
      protocol: 'vless',
      maxDownKbps: 10_000,
      trafficMultiplierBps: 10_000,
    };
    render(
      <SubConfigsTab
        links={[]}
        nodes={[
          { ...shared, accountIndex: 1, maxUpKbps: 5_000 },
          { ...shared, accountIndex: 2, maxUpKbps: 8_000 },
        ]}
        lang="en-US"
        now={1_700_000_000_000}
        onCopy={() => {}}
      />,
    );
    expect(screen.getAllByRole('heading', { name: 'Berlin' })).toHaveLength(2);
    expect(screen.getByText('Account 1')).toBeDefined();
    expect(screen.getByText('Account 2')).toBeDefined();
    expect(screen.queryByText(/@example\.com/)).toBeNull();
    expect(screen.getByText('5 Mbps')).toBeDefined();
    expect(screen.getByText('8 Mbps')).toBeDefined();
  });

  it('shows a red factor only when the server confirms a higher active overage factor', () => {
    const { container } = render(
      <SubConfigsTab
        links={[]}
        nodes={[
          {
            inboundId: 1,
            accountIndex: 1,
            remark: 'Active',
            protocol: 'vless',
            maxUpKbps: 0,
            maxDownKbps: 0,
            trafficMultiplierBps: 500_000,
            activeMultiplierBps: 3_700_000,
          },
          {
            inboundId: 2,
            accountIndex: 1,
            remark: 'Before quota',
            protocol: 'vless',
            maxUpKbps: 0,
            maxDownKbps: 0,
            trafficMultiplierBps: 500_000,
          },
          {
            inboundId: 3,
            accountIndex: 1,
            remark: 'Lower overage',
            protocol: 'vless',
            maxUpKbps: 0,
            maxDownKbps: 0,
            trafficMultiplierBps: 500_000,
            activeMultiplierBps: 200_000,
          },
        ]}
        lang="en-US"
        now={1_700_000_000_000}
        onCopy={() => {}}
      />,
    );
    const active = screen.getByRole('heading', { name: 'Active' }).closest('.sub-node-card');
    expect(active?.querySelector('.sub-multiplier-tag')?.textContent).toBe('50x');
    expect(active?.querySelector('.is-active-overage')?.textContent).toBe('370x');
    expect(container.querySelectorAll('.is-active-overage')).toHaveLength(1);
  });

  it('does not present an untracked TUIC or an unreachable node as a shared allowance', () => {
    render(
      <SubConfigsTab
        links={[]}
        nodes={[
          {
            inboundId: 11,
            accountIndex: 1,
            remark: 'TUIC',
            protocol: 'tuic',
            maxUpKbps: 0,
            maxDownKbps: 0,
            trafficMultiplierBps: 10_000,
            usageTracked: false,
          },
          {
            inboundId: 12,
            accountIndex: 1,
            remark: 'Remote',
            protocol: 'vless',
            maxUpKbps: 0,
            maxDownKbps: 0,
            trafficMultiplierBps: 10_000,
            usageTracked: true,
            windowConfigured: true,
          },
        ]}
        lang="en-US"
        now={1_700_000_000_000}
        onCopy={() => {}}
      />,
    );
    expect(screen.getByText('Individual usage is not available for this node')).toBeDefined();
    expect(screen.getByText('Usage is temporarily unavailable')).toBeDefined();
    expect(screen.queryByText('Uses your plan allowance')).toBeNull();
  });
});
