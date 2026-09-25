import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import SubHero from '@/pages/sub/SubHero';
import SubWindowCard from '@/pages/sub/SubWindowCard';

const hero = {
  status: 'active' as const,
  daysLeft: null,
  usedByte: 100,
  totalByte: 100,
  expireMs: 0,
  lastOnlineMs: 0,
  download: '100B',
  upload: '0B',
  used: '100B',
  total: '100B',
  remained: '0B',
  datepicker: 'gregorian' as const,
  lang: 'en-US',
};

describe('subscription exhausted quota presentation', () => {
  it('marks a fully used plan as alert even while overage service stays active', () => {
    const { container } = render(<SubHero {...hero} />);
    expect(container.querySelector('.sub-hero.is-alert')).not.toBeNull();
    expect(container.querySelector('.sub-ring-value')?.textContent).toBe('100.0%');
  });

  it('keeps the main ring red when service continues at reduced speed after quota exhaustion', () => {
    const { container } = render(<SubHero {...hero} status="throttled" />);
    const ringPath = container.querySelector<SVGElement>(
      '.sub-ring .ant-progress-circle-path[opacity="1"]',
    );
    expect(container.querySelector('.sub-hero.is-alert')).not.toBeNull();
    expect(ringPath?.style.stroke).toBe('rgb(255, 77, 79)');
  });

  it('marks a full window red using exact bytes, while one byte remaining is not exhausted', () => {
    const status = {
      quotaBytes: 1000,
      usedBytes: 1000,
      remainingBytes: 0,
      windowHours: 2,
      windowMode: 'fixed' as const,
      windowStart: 1_700_000_000_000,
      resetAt: 1_700_000_060_000,
    };
    const full = render(<SubWindowCard window={status} lang="en-US" now={status.windowStart} />);
    expect(full.container.querySelector('.sub-window-card.is-exhausted')).not.toBeNull();
    expect(full.container.querySelector('.ant-progress-status-normal')).not.toBeNull();
    expect(
      full.container.querySelector<SVGElement>(
        '.sub-window-ring .ant-progress-circle-path[opacity="1"]',
      )?.style.stroke,
    ).toBe('rgb(255, 77, 79)');
    full.unmount();
    const available = render(
      <SubWindowCard
        window={{ ...status, usedBytes: 999, remainingBytes: 1 }}
        lang="en-US"
        now={status.windowStart}
      />,
    );
    expect(available.container.querySelector('.sub-window-card.is-exhausted')).toBeNull();
  });
});
