'use client';

import { useTranslations } from 'next-intl';
import { Compass, Gauge, Hash, Layers, ListChecks, Radar, ShieldCheck, Timer, TimerOff, type LucideIcon } from 'lucide-react';
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
    const qualityFail = useSettingToggle(SettingKey.QualityFailEnabled);
    const autoRank = useSettingToggle(SettingKey.AutoRankEnabled);
    const channelFactor = useSettingToggle(SettingKey.AutoRankChannelFactorEnabled);
    const ttfb = useSettingToggle(SettingKey.AutoRankTTFBEnabled);
    const feedback = useSettingToggle(SettingKey.AutoRankFeedbackEnabled);
    const probe = useSettingToggle(SettingKey.AutoRankProbeEnabled);

    return (
        <SettingCard icon={ShieldCheck} title={t('reliability.title')}>
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

            {/* 质量失败检测（成功但输出异常→调度降权/冷却） */}
            <SettingSection title={t('qualityFail.title')} tooltip={t('qualityFail.hint')} />
            <SettingRow label={t('qualityFail.enabled.label')} tooltip={t('qualityFail.enabled.description')}>
                <Switch checked={qualityFail.enabled} onCheckedChange={qualityFail.toggle} />
            </SettingRow>
            {qualityFail.enabled && (
                <>
                    <NumberFieldRow
                        settingKey={SettingKey.QualityFailMinOutput}
                        label={t('qualityFail.minOutput.label')}
                        placeholder={t('qualityFail.minOutput.placeholder')}
                        icon={Hash}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.QualityFailMinMaxTokens}
                        label={t('qualityFail.minMaxTokens.label')}
                        placeholder={t('qualityFail.minMaxTokens.placeholder')}
                        icon={Hash}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.QualityFailCooldown}
                        label={t('qualityFail.cooldown.label')}
                        placeholder={t('qualityFail.cooldown.placeholder')}
                        icon={Timer}
                    />
                </>
            )}

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

                    {/* 公平调度门槛：竞技池准入与份额上限 */}
                    <SettingSection title={t('autoRank.thresholds.title')} tooltip={t('autoRank.thresholds.hint')} />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankSuccessGap}
                        label={t('autoRank.successGap.label')}
                        placeholder={t('autoRank.successGap.placeholder')}
                        tooltip={t('autoRank.successGap.description')}
                        icon={Gauge}
                        min={0}
                        max={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankLatencyRatio}
                        label={t('autoRank.latencyRatio.label')}
                        placeholder={t('autoRank.latencyRatio.placeholder')}
                        tooltip={t('autoRank.latencyRatio.description')}
                        icon={Gauge}
                        min={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankHealthThreshold}
                        label={t('autoRank.healthThreshold.label')}
                        placeholder={t('autoRank.healthThreshold.placeholder')}
                        tooltip={t('autoRank.healthThreshold.description')}
                        icon={Gauge}
                        min={0}
                        max={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankChannelMaxShare}
                        label={t('autoRank.channelMaxShare.label')}
                        placeholder={t('autoRank.channelMaxShare.placeholder')}
                        tooltip={t('autoRank.channelMaxShare.description')}
                        icon={Gauge}
                        min={1}
                        max={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankModelMaxShare}
                        label={t('autoRank.modelMaxShare.label')}
                        placeholder={t('autoRank.modelMaxShare.placeholder')}
                        tooltip={t('autoRank.modelMaxShare.description')}
                        icon={Gauge}
                        min={1}
                        max={100}
                    />
                    <NumberFieldRow
                        settingKey={SettingKey.AutoRankSoftmaxTemp}
                        label={t('autoRank.softmaxTemp.label')}
                        placeholder={t('autoRank.softmaxTemp.placeholder')}
                        tooltip={t('autoRank.softmaxTemp.description')}
                        icon={Gauge}
                        min={10}
                    />

                    {/* 实际分配反馈纠偏 */}
                    <SettingRow label={t('autoRank.feedback.label')} tooltip={t('autoRank.feedback.description')}>
                        <Switch checked={feedback.enabled} onCheckedChange={feedback.toggle} />
                    </SettingRow>
                    {feedback.enabled && (
                        <>
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankFeedbackEwma}
                                label={t('autoRank.feedbackEwma.label')}
                                placeholder={t('autoRank.feedbackEwma.placeholder')}
                                icon={Gauge}
                                min={1}
                                max={99}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankFeedbackTolerance}
                                label={t('autoRank.feedbackTolerance.label')}
                                placeholder={t('autoRank.feedbackTolerance.placeholder')}
                                icon={Gauge}
                                min={0}
                                max={100}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankFeedbackPenalty}
                                label={t('autoRank.feedbackPenalty.label')}
                                placeholder={t('autoRank.feedbackPenalty.placeholder')}
                                icon={Gauge}
                                min={0}
                                max={100}
                            />
                        </>
                    )}

                    {/* 主动探测：为欠采样候选补成功率样本（渠道需单独开启允许探测） */}
                    <SettingRow label={t('autoRank.probe.label')} tooltip={t('autoRank.probe.description')}>
                        <Switch checked={probe.enabled} onCheckedChange={probe.toggle} />
                    </SettingRow>
                    {probe.enabled && (
                        <>
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankProbeInterval}
                                label={t('autoRank.probeInterval.label')}
                                placeholder={t('autoRank.probeInterval.placeholder')}
                                tooltip={t('autoRank.probeInterval.description')}
                                icon={Radar}
                                min={1}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankProbeMaxPerRound}
                                label={t('autoRank.probeMaxPerRound.label')}
                                placeholder={t('autoRank.probeMaxPerRound.placeholder')}
                                tooltip={t('autoRank.probeMaxPerRound.description')}
                                icon={Radar}
                                min={1}
                            />
                            <NumberFieldRow
                                settingKey={SettingKey.AutoRankProbeCooldown}
                                label={t('autoRank.probeCooldown.label')}
                                placeholder={t('autoRank.probeCooldown.placeholder')}
                                tooltip={t('autoRank.probeCooldown.description')}
                                icon={Radar}
                                min={1}
                            />
                        </>
                    )}
                </>
            )}
        </SettingCard>
    );
}
