/// <reference types="vite/client" />

interface SubPageData {
  sId?: string;
  enabled?: boolean;
  download?: string;
  upload?: string;
  total?: string;
  used?: string;
  remained?: string;
  totalByte?: string | number;
  expire?: string | number;
  lastOnline?: string | number;
  subUrl?: string;
  subJsonUrl?: string;
  subClashUrl?: string;
  subTitle?: string;
  subSupportUrl?: string;
  subUpdates?: number;
  links?: string[];
  datepicker?: 'gregorian' | 'jalalian';
  announce?: string;
  downloadByte?: string | number;
  uploadByte?: string | number;
  billedDownByte?: string | number;
  billedUpByte?: string | number;
  usedByte?: string | number;
  windowQuota?: SubWindowStatus | null;
  windowQuotas?: SubAccountWindowQuota[];
  accountStates?: SubAccountPublicStatus[];
  nodes?: SubNodeOverview[];
  publicState?: 'active' | 'grace' | 'blocked' | 'mixed';
}

interface SubAccountPublicStatus {
  accountIndex: number;
  state: 'active' | 'grace' | 'blocked';
  expiryMs: number;
}

interface SubNodeOverview {
  inboundId: number;
  accountIndex: number;
  remark: string;
  protocol: string;
  maxUpKbps: number;
  maxDownKbps: number;
  trafficMultiplierBps: number;
  usageTracked?: boolean;
  windowConfigured?: boolean;
  window?: SubWindowStatus | null;
}

interface SubAccountWindowQuota {
  accountIndex: number;
  window: SubWindowStatus;
}

interface SubWindowStatus {
  quotaBytes: number;
  usedBytes: number;
  remainingBytes: number;
  windowHours: number;
  windowMode: 'fixed' | 'rolling';
  windowStart: number;
  resetAt: number;
}

interface Window {
  X_UI_BASE_PATH?: string;
  X_UI_CUR_VER?: string;
  X_UI_DB_TYPE?: string;
  __SUB_PAGE_DATA__?: SubPageData;
}

declare module 'persian-calendar-suite' {
  import type { ComponentType, ReactNode } from 'react';

  type DateInput = string | number | null;
  type OutputFormat = 'iso' | 'shamsi' | 'gregorian' | 'hijri' | 'timestamp';

  interface PersianDateTimePickerProps {
    value?: DateInput;
    onChange?: (value: number | string | null) => void;
    defaultValue?: string | number | 'now' | null;
    showTime?: boolean;
    minuteStep?: number;
    outputFormat?: OutputFormat;
    showFooter?: boolean;
    theme?: Record<string, unknown>;
    disabledHours?: number[];
    minDate?: string | Date | null;
    maxDate?: string | Date | null;
    enabledDates?: string[] | null;
    disabledDates?: string[] | null;
    disabledWeekDays?: number[];
    persianNumbers?: boolean;
    rtlCalendar?: boolean;
    placeholder?: string;
    disabled?: boolean;
    className?: string;
    children?: ReactNode;
  }

  export const PersianDateTimePicker: ComponentType<PersianDateTimePickerProps>;
  export const PersianCalendar: ComponentType<Record<string, unknown>>;
  export const PersianDateRangePicker: ComponentType<Record<string, unknown>>;
  export const PersianTimePicker: ComponentType<Record<string, unknown>>;
  export const PersianTimeline: ComponentType<Record<string, unknown>>;
}
