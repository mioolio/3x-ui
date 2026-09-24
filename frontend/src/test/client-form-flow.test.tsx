import { afterEach, describe, it, expect, vi } from 'vitest';
import { act, render, fireEvent, waitFor, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { message } from 'antd';

import { ThemeProvider } from '@/hooks/useTheme';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import type { ClientRecord, InboundOption } from '@/hooks/useClients';
import { HttpUtil } from '@/utils';
import { setMessageInstance } from '@/utils/messageBus';

afterEach(() => {
  vi.restoreAllMocks();
  setMessageInstance(message as never);
});

function makeQC() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

const REALITY_INBOUND = {
  id: 4,
  port: 10443,
  protocol: 'vless',
  tag: 'in-10443-tcp',
  tlsFlowCapable: true,
  enable: true,
} as unknown as InboundOption;

const MTPROTO_INBOUND = {
  id: 5,
  port: 10444,
  protocol: 'mtproto',
  tag: 'mtproto-test',
  enable: true,
} as unknown as InboundOption;

const TUIC_INBOUND = {
  id: 6,
  port: 10445,
  protocol: 'tuic',
  tag: 'tuic-test',
  enable: true,
} as unknown as InboundOption;

const CLIENT = {
  email: 'testuser',
  flow: 'xtls-rprx-vision',
  uuid: '11111111-1111-1111-1111-111111111111',
  subId: 'subid123',
  enable: true,
} as unknown as ClientRecord;

function savedFlow(save: ReturnType<typeof vi.fn>): unknown {
  return (save.mock.calls[0][0] as Record<string, unknown>).flow;
}

function mockLinkedSettings() {
  return vi.spyOn(HttpUtil, 'get').mockResolvedValue({ success: true, obj: {} } as never);
}

async function waitForLinkedSettings(get: ReturnType<typeof mockLinkedSettings>) {
  await waitFor(() => {
    expect(get).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/directionalRates',
      undefined,
      expect.anything(),
    );
    expect(get).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/windowQuotas',
      undefined,
      expect.anything(),
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

describe('ClientFormModal — Vision flow preservation', () => {
  it('keeps xtls-rprx-vision with a stable Reality inbound', async () => {
    const qc = makeQC();
    const save = vi.fn().mockResolvedValue({ success: true });
    const get = mockLinkedSettings();
    render(
      <ThemeProvider>
        <QueryClientProvider client={qc}>
          <ClientFormModal
            open
            mode="edit"
            client={CLIENT}
            inbounds={[REALITY_INBOUND]}
            attachedIds={[4]}
            save={save}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    await waitForLinkedSettings(get);
    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedFlow(save)).toBe('xtls-rprx-vision');
  });

  it('does not drop a selected Vision flow while the inbound options momentarily reload', async () => {
    const qc = makeQC();
    const save = vi.fn().mockResolvedValue({ success: true });
    const get = mockLinkedSettings();
    const tree = (inbounds: InboundOption[]) => (
      <ThemeProvider>
        <QueryClientProvider client={qc}>
          <ClientFormModal
            open
            mode="edit"
            client={CLIENT}
            inbounds={inbounds}
            attachedIds={[4]}
            save={save}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>
    );
    // Options loaded -> reloading (inboundOptionsQuery.data ?? [] === []) -> loaded again.
    const { rerender } = render(tree([REALITY_INBOUND]));
    await waitForLinkedSettings(get);
    await screen.findByRole('button', { name: /save/i });
    rerender(tree([]));
    rerender(tree([REALITY_INBOUND]));

    fireEvent.click(await screen.findByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedFlow(save)).toBe('xtls-rprx-vision');
  });
});

describe('ClientFormModal — partial node policy save', () => {
  it('closes a newly created client and warns that its node policy needs an upgrade', async () => {
    const save = vi.fn().mockResolvedValue({ success: true, msg: 'Client added' });
    const onOpenChange = vi.fn();
    const warning = vi.fn();
    setMessageInstance({ warning } as never);
    vi.spyOn(HttpUtil, 'post').mockImplementation(async (path) => {
      if (String(path).includes('/directionalRates')) {
        return { success: false, msg: '节点需升级后才能应用入站限速' } as never;
      }
      return { success: true, obj: {} } as never;
    });
    render(
      <ThemeProvider>
        <QueryClientProvider client={makeQC()}>
          <ClientFormModal
            open
            mode="add"
            client={null}
            inbounds={[REALITY_INBOUND]}
            save={save}
            onOpenChange={onOpenChange}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    fireEvent.click(await screen.findByRole('tab', { name: /^attached inbounds$/i }));
    fireEvent.click(await screen.findByRole('button', { name: /^select all$/i }));
    fireEvent.click(screen.getByRole('button', { name: /^create$/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    await waitFor(() => expect(warning).toHaveBeenCalledTimes(1));
    const content = String((warning.mock.calls[0][0] as { content: unknown }).content);
    expect(content).toContain('Client created, but its linked inbound policy was not applied');
    expect(content).toContain('节点需升级');
  }, 15000);
});

describe('ClientFormModal — linked inbound policy loading', () => {
  it('waits for both policy responses before showing editable fields and preserves loaded values', async () => {
    const quotaBytes = 1024 * 1024 * 1024 + 123;
    let resolveRates!: (result: unknown) => void;
    let resolveQuotas!: (result: unknown) => void;
    vi.spyOn(HttpUtil, 'get').mockImplementation((path) => {
      if (String(path).endsWith('/directionalRates')) {
        return new Promise((resolve) => {
          resolveRates = resolve;
        }) as never;
      }
      if (String(path).endsWith('/windowQuotas')) {
        return new Promise((resolve) => {
          resolveQuotas = resolve;
        }) as never;
      }
      return Promise.resolve({ success: true, obj: {} }) as never;
    });
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue({ success: true, obj: {} } as never);
    const save = vi.fn().mockResolvedValue({ success: true });
    render(
      <ThemeProvider>
        <QueryClientProvider client={makeQC()}>
          <ClientFormModal
            open
            mode="edit"
            client={CLIENT}
            inbounds={[REALITY_INBOUND]}
            attachedIds={[4]}
            save={save}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    fireEvent.click(await screen.findByRole('tab', { name: /^attached inbounds$/i }));
    expect(screen.queryByText("This inbound's maximum upload (0 = no maximum)")).toBeNull();

    await act(async () => {
      resolveRates({ success: true, obj: { 4: { upKbps: 512, downKbps: 2048 } } });
    });
    expect(screen.queryByText("This inbound's maximum upload (0 = no maximum)")).toBeNull();
    await act(async () => {
      resolveQuotas({
        success: true,
        obj: { 4: { quotaBytes, hours: 2, mode: 'fixed', windowExhaustAction: 'stop' } },
      });
    });
    await screen.findByText("This inbound's maximum upload (0 = no maximum)");

    fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        post.mock.calls.filter(([path]) => /\/(directionalRates|windowQuotas)$/.test(String(path))),
      ).toHaveLength(2),
    );
    expect(post).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/directionalRates',
      { rates: { 4: { upKbps: 512, downKbps: 2048 } } },
      expect.anything(),
    );
    expect(post).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/windowQuotas',
      { quotas: { 4: expect.objectContaining({ quotaBytes }) } },
      expect.anything(),
    );
  });
});

describe('ClientFormModal — sidecar capabilities', () => {
  it('hides unsupported per-client controls and leaves their stored values untouched', async () => {
    const get = vi.spyOn(HttpUtil, 'get').mockImplementation((path) => {
      if (String(path).endsWith('/directionalRates')) {
        return Promise.resolve({
          success: true,
          obj: { 5: { upKbps: 500, downKbps: 800 }, 6: { upKbps: 700, downKbps: 900 } },
        }) as never;
      }
      if (String(path).endsWith('/windowQuotas')) {
        return Promise.resolve({
          success: true,
          obj: {
            5: { quotaBytes: 2 * 1024 ** 3, hours: 2, mode: 'fixed' },
            6: { quotaBytes: 3 * 1024 ** 3, hours: 3, mode: 'rolling' },
          },
        }) as never;
      }
      return Promise.resolve({ success: true, obj: {} }) as never;
    });
    const post = vi.spyOn(HttpUtil, 'post').mockResolvedValue({ success: true, obj: {} } as never);
    const save = vi.fn().mockResolvedValue({ success: true });
    render(
      <ThemeProvider>
        <QueryClientProvider client={makeQC()}>
          <ClientFormModal
            open
            mode="edit"
            client={CLIENT}
            inbounds={[MTPROTO_INBOUND, TUIC_INBOUND]}
            attachedIds={[5, 6]}
            save={save}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    await waitForLinkedSettings(get);
    fireEvent.click(screen.getByRole('tab', { name: /^attached inbounds$/i }));
    await screen.findByText(/MTProto and TUIC cannot enforce per-client speed maxima/);
    expect(screen.queryByText('Maximum speed by inbound')).toBeNull();
    expect(screen.getByText(/MTProto windows can only stop service/)).toBeTruthy();
    expect(screen.getAllByText('Window traffic (GB, 0 = unlimited)')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: /^save$/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        post.mock.calls.filter(([path]) => /\/(directionalRates|windowQuotas)$/.test(String(path))),
      ).toHaveLength(2),
    );
    expect(post).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/directionalRates',
      { rates: {} },
      expect.anything(),
    );
    expect(post).toHaveBeenCalledWith(
      '/panel/api/clients/testuser/windowQuotas',
      { quotas: { 5: expect.objectContaining({ quotaBytes: 2 * 1024 ** 3 }) } },
      expect.anything(),
    );
  });
});

describe('ClientFormModal — expiry continuation', () => {
  it.each(['mtproto', 'tuic', 'amneziawg'])(
    'warns that %s cannot be relied on for low-speed expiry continuation',
    async (protocol) => {
      mockLinkedSettings();
      const inbound = {
        id: 20,
        port: 10446,
        protocol,
        tag: `${protocol}-grace-test`,
        enable: true,
      } as unknown as InboundOption;
      render(
        <ThemeProvider>
          <QueryClientProvider client={makeQC()}>
            <ClientFormModal
              open
              mode="edit"
              client={{ ...CLIENT, expiryTime: Date.now() + 7 * 86400000 }}
              inbounds={[inbound]}
              attachedIds={[20]}
              save={vi.fn()}
              onOpenChange={() => {}}
            />
          </QueryClientProvider>
        </ThemeProvider>,
      );
      expect(
        await screen.findByText(
          /Expiry low-speed continuation applies to supported patched Xray inbounds, including WireGuard/,
        ),
      ).toBeTruthy();
    },
  );

  it('does not warn for a WireGuard peer, which uses the patched Xray policy', async () => {
    mockLinkedSettings();
    const inbound = {
      id: 21,
      port: 10447,
      protocol: 'wireguard',
      tag: 'wireguard-grace-test',
      enable: true,
    } as unknown as InboundOption;
    render(
      <ThemeProvider>
        <QueryClientProvider client={makeQC()}>
          <ClientFormModal
            open
            mode="edit"
            client={{ ...CLIENT, expiryTime: Date.now() + 7 * 86400000 }}
            inbounds={[inbound]}
            attachedIds={[21]}
            save={vi.fn()}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    await screen.findByText('After expiry');
    expect(screen.queryByText(/Expiry low-speed continuation applies/)).toBeNull();
  });

  it('saves a two-day low-speed period without requiring an extra traffic allowance', async () => {
    const save = vi.fn().mockResolvedValue({ success: true });
    const get = mockLinkedSettings();
    vi.spyOn(HttpUtil, 'post').mockResolvedValue({ success: true, obj: {} } as never);
    render(
      <ThemeProvider>
        <QueryClientProvider client={makeQC()}>
          <ClientFormModal
            open
            mode="edit"
            client={{ ...CLIENT, expiryTime: Date.now() + 7 * 86400000 }}
            inbounds={[REALITY_INBOUND]}
            attachedIds={[4]}
            save={save}
            onOpenChange={() => {}}
          />
        </QueryClientProvider>
      </ThemeProvider>,
    );
    await waitForLinkedSettings(get);
    const action = screen
      .getByText('Continue at low speed after expiry')
      .closest('.ant-form-item')
      ?.querySelector('[role="switch"]') as HTMLElement | null;
    expect(action).toBeTruthy();
    fireEvent.click(action!);
    const days = screen
      .getByText('Continuation days')
      .closest('.ant-form-item')
      ?.querySelector('input') as HTMLInputElement | null;
    expect(days).toBeTruthy();
    fireEvent.change(days!, { target: { value: '2' } });
    fireEvent.blur(days!);
    fireEvent.click(screen.getByRole('button', { name: /save/i }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0][0]).toMatchObject({
      graceHours: 48,
      graceUpKbps: 128,
      graceDownKbps: 128,
      graceQuotaBytes: 0,
    });
  });
});
