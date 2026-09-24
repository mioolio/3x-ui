/**
 * Display label for an inbound: the remark when one is set, otherwise the
 * inbound tag. Falls back to an empty string when neither is present.
 */
export function formatInboundLabel(tag?: string, remark?: string): string {
  const remarkText = (remark || '').trim();
  if (remarkText) return remarkText;
  return (tag || '').trim();
}

/** Include the physical node and database ID when choosing where to attach a client. */
export function formatInboundOptionLabel(inbound: {
  id: number;
  tag?: string;
  remark?: string;
  nodeId?: number | null;
  nodeName?: string;
  nodeAddress?: string;
}): string {
  const name = formatInboundLabel(inbound.tag, inbound.remark);
  if (inbound.nodeId == null) return name ? `${name} · #${inbound.id}` : `#${inbound.id}`;
  const node = inbound.nodeName?.trim() || inbound.nodeAddress?.trim() || `node #${inbound.nodeId}`;
  return name ? `${name} · ${node} · #${inbound.id}` : `${node} · #${inbound.id}`;
}

export function formatTunnelConfigMeta(
  inbound: { id?: number; tag?: string; remark?: string },
  email?: string,
  totalCount = 1,
): {
  label?: string;
  fileName: string;
  qrRemark: string;
} {
  const inboundName =
    formatInboundLabel(inbound.tag, inbound.remark) ||
    (inbound.id != null ? `inbound-${inbound.id}` : '');
  const label = totalCount > 1 ? inboundName : undefined;
  const suffix = inbound.remark || inbound.tag || (inbound.id != null ? `${inbound.id}` : '');
  const safeSuffix = suffix ? `-${suffix.replace(/[^\w.-]+/g, '_')}` : '';
  const emailPrefix = email || 'client';
  const fileName = `${emailPrefix}${totalCount > 1 ? safeSuffix : ''}.conf`;
  const qrRemark =
    totalCount > 1 && inboundName ? [inboundName, email].filter(Boolean).join(' - ') : email || '';

  return { label, fileName, qrRemark };
}
