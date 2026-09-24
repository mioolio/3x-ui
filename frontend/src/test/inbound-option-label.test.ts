import { describe, expect, it } from 'vitest';

import { formatInboundOptionLabel } from '@/lib/inbounds/label';

describe('formatInboundOptionLabel', () => {
  it('distinguishes same-named inbounds on different nodes', () => {
    const first = formatInboundOptionLabel({ id: 31, remark: 'Free', nodeId: 2, nodeName: 'B' });
    const second = formatInboundOptionLabel({ id: 44, remark: 'Free', nodeId: 3, nodeName: 'C' });
    expect(first).toBe('Free · B · #31');
    expect(second).toBe('Free · C · #44');
  });

  it('keeps same-named rows distinguishable even within one node', () => {
    expect(formatInboundOptionLabel({ id: 31, remark: 'Free', nodeId: 2, nodeName: 'B' })).not.toBe(
      formatInboundOptionLabel({ id: 32, remark: 'Free', nodeId: 2, nodeName: 'B' }),
    );
  });

  it('falls back to a node address or id when the node has no name', () => {
    expect(
      formatInboundOptionLabel({ id: 31, tag: 'tag-a', nodeId: 2, nodeAddress: 'b.test' }),
    ).toBe('tag-a · b.test · #31');
    expect(formatInboundOptionLabel({ id: 32, tag: 'tag-b', nodeId: 2 })).toBe(
      'tag-b · node #2 · #32',
    );
    expect(formatInboundOptionLabel({ id: 33, tag: 'tag-c' })).toBe('tag-c · #33');
    expect(formatInboundOptionLabel({ id: 34 })).toBe('#34');
  });
});
