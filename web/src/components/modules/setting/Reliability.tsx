'use client';

import { useTranslations } from 'next-intl';
import { Compass, Gauge, Hash, HeartPulse, Layers, ListChecks, ShieldCheck, Timer, TimerOff, type LucideIcon } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { SettingKey } from '@/api/endpoints/setting';
import { SettingCard, SettingRow, SettingSection, useSettingField, useSettingToggle } from './shared';

// min/max 与后端 model.Setting.Validate() 的边界保持一致，前端先行约束整数范围。
const OUTLIER_FIELDS: { key: string; labelKey: string; min: number; max?: number }[] = [
    { key: SettingKey.OutlierRetireInterval, labelKey: 'interval', min: 1 },
    { key: SettingKey.OutlierFailRatePct, labelKey: 'failRate', min: 1, max: 100 },
    { key: SettingKey.OutlierMinSamples, labelKey: 'minSamples', min: 1 },
    { key: SettingKey.OutlierConsecFails, labelKey: 'consecFails', min: 1 },
    { key: SettingKey.OutlierWindowMinutes, labelKey: 'windowMinutes', min: 1 },
    { key: SettingKey.OutlierWindowCapacity, labelKey: 'windowCapacity', min: 1, max: 20 },
    { key: SettingKey.OutlierRecoverStreak, labelKey: 'recoverStreak', min: 1 },
    { key: SettingKey.OutlierReapMinutes, labelKey: 'reapMinutes', min: 1 },
    { key: SettingKey.OutlierCFRecoverMinutes, labelKey: 'cfRecoverMinutes', min: 1 },
];

function NumberFieldRow({ settingKey, label, placeholder, tooltip, icon, min, max }: {
    settingKey: string;
    label: string;
    placeholder: string;
    tooltip?: React.ReactNode;
    icon?: LucideIcon;
    min?: number;
    max?: number;
}) {
    const field = useSettingField(settingKey);
    return (
        <SettingRow icon={icon} label={label} tooltip={tooltip}>
            <Input
                type="number"
                step={1}
                min={min}
                max={max}
                value={field.value}
                onChange={(e) => field.setValue(e.target.value)}
                onBlur={field.save}
                placeholder={placeholder}
                className="w-48 rounded-xl"
            />
        </SettingRow>
    );
}

export function SettingReliability() {
    const t = useTranslations('setting');
    const outlier = useSettingToggle(SettingKey.OutlierRetireEnabled);
    const groupHealth = useSettingToggle(SettingKey.GroupHealthEnabled);
    const autoRank = useSettingToggle(SettingKey.AutoRankEnabled);
    const channelFactor = useSettingToggle(SettingKey.AutoRankChannelFactorEnabled);
    const ttfb = useSettingToggle(SettingKey.AutoRankTTFBEnabled);

    return (
        <SettingCard icon={ShieldCheck} title={t('reliability.title')}>
            {/* 分组健康检查 */}
            <SettingRow icon={HeartPulse} label={t('groupHealth.label')} tooltip={t('groupHealth.description')}>
                <Switch checked={groupHealth.enabled} onCheckedChange={groupHealth.toggle} />
            </SettingRow>

            {/* 熔断器 */}
            <SettingSection title={t('circuitBreaker.title')} tooltip={t('circuitBreaker.hint')} />
            <NumberFieldRow
                settingKey={SettingKey.CircuitBreakerThreshold}
                label={t('circuitBreaker.threshold.label')}
                placeholder={t('circuitBreaker.threshold.placeholder')}
                icon={Hash}
            />
            <NumberFieldRow
                settingKey={SettingKey.CircuitBreakerCooldown}
                label={t('circuitBreaker.cooldown.label')}
                placeholder={t('circuitBreaker.cooldown.placeholder')}
                icon={Timer}
            />
            <NumberFieldRow
                settingKey={SettingKey.CircuitBreakerMaxCooldown}
                label={t('circuitBreaker.maxCooldown.label')}
                placeholder={t('circuitBreaker.maxCooldown.placeholder')}
                icon={TimerOff}
            />

            {/* 被动离群退役 */}
            <SettingSection title={t('outlierRetirement.title')} tooltip={t('outlierRetirement.hint')} />
            <SettingRow label={t('outlierRetirement.enabled.label')}>
                <Switch checked={outlier.enabled} onCheckedChange={outlier.toggle} />
            </SettingRow>
            {outlier.enabled && OUTLIER_FIELDS.map((f) => (
                <NumberFieldRow
                    key={f.key}
                    settingKey={f.key}
                    label={t(`outlierRetirement.${f.labelKey}.label`)}
                    placeholder={t(`outlierRetirement.${f.labelKey}.placeholder`)}
                    tooltip={t(`outlierRetirement.${f.labelKey}.description`)}
                    min={f.min}
                    max={f.max}
                />
            ))}

            {/* 自动排序（Auto 模式） */}
            <SettingSection title={t('autoRank.title')} tooltip={t('autoRank.hint')} />
            <SettingRow label={t('autoRank.enabled.label')} tooltip={t('autoRank.enabled.description')}>
                <Switch checked={autoRank.enabled} onCheckedChange={autoRank.toggle} />
            </SettingRow>
            {autoRank.enabled && (
                <>
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankExploreRatio}
                        label={t('autoRank.exploreRatio.label')}
                        placeholder={t('autoRank.exploreRatio.placeholder')}
                        tooltip={t('autoRank.exploreRatio.description')}
                        icon={Compass}
                        min={0}
                        max={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankMinSamples}
                        label={t('autoRank.minSamples.label')}
                        placeholder={t('autoRank.minSamples.placeholder')}
                        tooltip={t('autoRank.minSamples.description')}
                        icon={ListChecks}
                        min={1}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankInterval}
                        label={t('autoRank.interval.label')}
                        placeholder={t('autoRank.interval.placeholder')}
                        tooltip={t('autoRank.interval.description')}
                        icon={Timer}
                        min={1}
                    />

                    <SettingRow label={t('autoRank.channelFactor.label')} tooltip={t('autoRank.channelFactor.description')}>
                        <Switch checked={channelFactor.enabled} onCheckedChange={channelFactor.toggle} />
                    </SettingRow>
                    {channelFactor.enabled && (
                        <>
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankChannelMinSamples}
                                label={t('autoRank.channelMinSamples.label')}
                                placeholder={t('autoRank.channelMinSamples.placeholder')}
                                icon={Layers}
                                min={1}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankChannelMinModels}
                                label={t('autoRank.channelMinModels.label')}
                                placeholder={t('autoRank.channelMinModels.placeholder')}
                                icon={Layers}
                                min={2}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankChannelDegradeRate}
                                label={t('autoRank.channelDegradeRate.label')}
                                placeholder={t('autoRank.channelDegradeRate.placeholder')}
                                icon={Layers}
                                min={0}
                                max={100}
                            />
                        </>
                    )}

                    <SettingRow label={t('autoRank.ttfb.label')} tooltip={t('autoRank.ttfb.description')}>
                        <Switch checked={ttfb.enabled} onCheckedChange={ttfb.toggle} />
                    </SettingRow>
                    {ttfb.enabled && (
                        <>
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankTTFBWeight}
                                label={t('autoRank.ttfbWeight.label')}
                                placeholder={t('autoRank.ttfbWeight.placeholder')}
                                icon={Gauge}
                                min={0}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankTTFBMaxSlowRatio}
                                label={t('autoRank.ttfbMaxSlowRatio.label')}
                                placeholder={t('autoRank.ttfbMaxSlowRatio.placeholder')}
                                icon={Gauge}
                                min={0}
                                max={100}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankTTFBMinConfidentSample}
                                label={t('autoRank.ttfbMinConfidentSample.label')}
                                placeholder={t('autoRank.ttfbMinConfidentSample.placeholder')}
                                icon={Gauge}
                                min={1}
                            />
                        </>
                    )}
                </>
            )}
        </SettingCard>
    );
}
