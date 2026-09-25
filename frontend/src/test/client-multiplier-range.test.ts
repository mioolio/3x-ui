/// <reference types="vite/client" />
import { describe, expect, it } from 'vitest';

import { ClientFormSchema } from '@/schemas/client';
import {
  MAX_TRAFFIC_MULTIPLIER_BPS,
  trafficMultiplierBpsToInput,
  trafficMultiplierInputToBps,
} from '@/lib/xray/traffic-multiplier';

describe('custom overage multipliers', () => {
  it('preserves 370× and the largest exactly representable multiplier', () => {
    for (const input of ['370', '900719925474.0991']) {
      const bps = trafficMultiplierInputToBps(input);
      expect(trafficMultiplierBpsToInput(bps)).toBe(input);
      expect(ClientFormSchema.shape.totalOverageMultiplierBps.safeParse(bps).success).toBe(true);
      expect(ClientFormSchema.shape.windowOverageMultiplierBps.safeParse(bps).success).toBe(true);
    }
  });

  it('rejects values that would be rounded or cannot be stored exactly', () => {
    for (const input of ['0.99', '370.00001', '900719925474.0992']) {
      const bps = trafficMultiplierInputToBps(input);
      expect(ClientFormSchema.shape.windowOverageMultiplierBps.safeParse(bps).success).toBe(false);
    }
    expect(
      ClientFormSchema.shape.windowOverageMultiplierBps.safeParse(MAX_TRAFFIC_MULTIPLIER_BPS + 1)
        .success,
    ).toBe(false);
  });
});
