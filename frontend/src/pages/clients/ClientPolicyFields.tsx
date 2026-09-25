import { useState } from 'react';
import { Col, InputNumber, Row, Select, Typography } from 'antd';
import { useFormContext, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';

import { FormField } from '@/components/form/rhf';
import {
  MAX_TRAFFIC_MULTIPLIER_INPUT,
  trafficMultiplierBpsToInput,
  trafficMultiplierInputToBps,
} from '@/lib/xray/traffic-multiplier';

type ExhaustScope = 'total' | 'window';
type SpeedDirection = 'Up' | 'Down';
type SpeedUnit = 'Mbps' | 'Kbps';
type SpeedFieldName =
  | 'totalExhaustUpKbps'
  | 'totalExhaustDownKbps'
  | 'windowExhaustUpKbps'
  | 'windowExhaustDownKbps';

export interface ClientPolicyFormValues {
  totalExhaustAction: 'stop' | 'throttle';
  totalExhaustUpKbps: number;
  totalExhaustDownKbps: number;
  totalOverageMultiplierBps: number;
  windowExhaustAction: 'stop' | 'throttle';
  windowExhaustUpKbps: number;
  windowExhaustDownKbps: number;
  windowOverageMultiplierBps: number;
}

export function PolicySpeedInput({
  value = 0,
  onChange,
}: {
  value?: number;
  onChange?: (value: number) => void;
}) {
  const [unit, setUnit] = useState<SpeedUnit>('Mbps');
  return (
    <InputNumber
      value={value / (unit === 'Mbps' ? 1000 : 1)}
      onChange={(next) =>
        onChange?.(Math.round((Number(next) || 0) * (unit === 'Mbps' ? 1000 : 1)))
      }
      min={0}
      max={unit === 'Mbps' ? 1_000_000 : 1_000_000_000}
      precision={unit === 'Mbps' ? 3 : 0}
      addonAfter={
        <Select
          value={unit}
          options={[{ value: 'Mbps' }, { value: 'Kbps' }]}
          onChange={(next) => setUnit(next)}
          style={{ width: 92 }}
        />
      }
      style={{ width: '100%' }}
    />
  );
}

function PolicySpeedField({
  name,
  direction,
}: {
  name: SpeedFieldName;
  direction: SpeedDirection;
}) {
  const { t } = useTranslation();
  return (
    <FormField
      name={name}
      label={t(`pages.clients.policy.${direction === 'Up' ? 'upload' : 'download'}`)}
    >
      <PolicySpeedInput />
    </FormField>
  );
}

function ExhaustPolicy({ scope }: { scope: ExhaustScope }) {
  const { t } = useTranslation();
  const { control } = useFormContext<ClientPolicyFormValues>();
  const actionName = scope === 'total' ? 'totalExhaustAction' : 'windowExhaustAction';
  const action = useWatch({ control, name: actionName });
  const multiplierName =
    scope === 'total' ? 'totalOverageMultiplierBps' : 'windowOverageMultiplierBps';
  const prefix = scope === 'total' ? 'totalExhaust' : 'windowExhaust';

  return (
    <section>
      <Typography.Title level={5}>
        {t(`pages.clients.policy.${scope === 'total' ? 'totalTitle' : 'windowTitle'}`)}
      </Typography.Title>
      <FormField name={actionName} label={t('pages.clients.policy.afterQuota')}>
        <Select
          options={[
            { value: 'stop', label: t('pages.clients.policy.stop') },
            { value: 'throttle', label: t('pages.clients.policy.throttle') },
          ]}
        />
      </FormField>
      {action === 'throttle' && (
        <>
          <Typography.Paragraph type="secondary">
            {t('pages.clients.policy.throttleHint')}
          </Typography.Paragraph>
          <Row gutter={12}>
            <Col xs={24} md={12}>
              <PolicySpeedField name={`${prefix}UpKbps`} direction="Up" />
            </Col>
            <Col xs={24} md={12}>
              <PolicySpeedField name={`${prefix}DownKbps`} direction="Down" />
            </Col>
          </Row>
          <FormField
            name={multiplierName}
            label={t('pages.clients.policy.multiplier')}
            extra={t('pages.clients.policy.multiplierHint')}
            transform={{
              input: trafficMultiplierBpsToInput,
              output: trafficMultiplierInputToBps,
            }}
          >
            <InputNumber<string>
              stringMode
              min="1"
              max={MAX_TRAFFIC_MULTIPLIER_INPUT}
              step="0.01"
              precision={4}
              addonAfter="×"
              style={{ width: '100%' }}
            />
          </FormField>
        </>
      )}
    </section>
  );
}

export default function ClientPolicyFields() {
  const { t } = useTranslation();

  return (
    <>
      <Typography.Paragraph type="secondary">
        {t('pages.inbounds.xrayOnlyHint')}
      </Typography.Paragraph>
      <ExhaustPolicy scope="total" />
      <ExhaustPolicy scope="window" />
    </>
  );
}
