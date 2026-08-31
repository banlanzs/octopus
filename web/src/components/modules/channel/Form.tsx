import { ChannelType, type AutoGroupType, type Channel, type ChannelGroup, type ChannelGroupItem, type ChannelTestResult, type ChannelWSMode, type ModelRedirect, useFetchModel, useTestChannel } from '@/api/endpoints/channel';
import { GroupMode } from '@/api/endpoints/group';
import { ProxySelector } from '@/components/modules/proxy-pool/ProxySelector';
import { TagInput } from '@/components/modules/site/TagInput';
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from '@/components/ui/select';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Switch } from '@/components/ui/switch';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { toast } from '@/components/common/Toast';
import { useTranslations } from 'next-intl';
import { useEffect, useRef, useState } from 'react';
import { RefreshCw, X, Plus, FlaskConical, Loader2, Import } from 'lucide-react';
import { parseChannelKeyImportContent } from './key-import';

export interface ChannelKeyFormItem {
    id?: number;
    enabled: boolean;
    channel_key: string;
    status_code?: number;
    last_use_time_stamp?: number;
    total_cost?: number;
    remark?: string;
}

export interface ChannelFormData {
    name: string;
    type: ChannelType;
    base_urls: Channel['base_urls'];
    custom_header: Channel['custom_header'];
    ws_mode: ChannelWSMode;
    proxy_mode: Channel['proxy_mode'];
    proxy_config_id: number | null;
    param_override: string;
    keys: ChannelKeyFormItem[];
    model: string;
    custom_model: string;
    tags: string[];
    model_redirects: ModelRedirect[];
    model_redirect_only: boolean;
    channel_groups: ChannelGroup[];
    enabled: boolean;
    auto_sync: boolean;
    force_deep_seek_thinking: boolean;
    probe_enabled: boolean;
    scheduling_exempt: boolean;
    auto_group: AutoGroupType;
    match_regex: string;
}

export interface ChannelFormProps {
    formData: ChannelFormData;
    onFormDataChange: (data: ChannelFormData) => void;
    onSubmit: (event: React.FormEvent<HTMLFormElement>) => void;
    isPending: boolean;
    submitText: string;
    pendingText: string;
    onCancel?: () => void;
    cancelText?: string;
    idPrefix?: string;
}

import {
    Accordion,
    AccordionContent,
    AccordionItem,
    AccordionTrigger,
} from "@/components/ui/accordion";

// ModelCombobox 模型名输入框：聚焦时弹出已拉取模型候选列表，点击填入；
// 候选列表外仍可手动输入任意模型名。
function ModelCombobox({
    value,
    onChange,
    candidates,
    placeholder,
}: {
    value: string;
    onChange: (value: string) => void;
    candidates: string[];
    placeholder?: string;
}) {
    const t = useTranslations('channel.form');
    const [open, setOpen] = useState(false);
    const normalized = value.trim().toLowerCase();
    const matches = candidates.filter((c) => c.trim().toLowerCase().includes(normalized));

    return (
        <Popover open={open} onOpenChange={setOpen}>
            <PopoverTrigger asChild>
                <div className="flex-1">
                    <Input
                        type="text"
                        value={value}
                        onChange={(e) => onChange(e.target.value)}
                        onFocus={() => setOpen(true)}
                        placeholder={placeholder}
                        className="rounded-xl w-full"
                    />
                </div>
            </PopoverTrigger>
            <PopoverContent
                align="start"
                sideOffset={4}
                className="w-64 p-1.5 max-h-56 overflow-y-auto"
                onOpenAutoFocus={(e) => e.preventDefault()}
            >
                {matches.length > 0 ? (
                    matches.map((c) => (
                        <button
                            key={c}
                            type="button"
                            onClick={() => {
                                onChange(c);
                                setOpen(false);
                            }}
                            className="w-full text-left px-2.5 py-1.5 rounded-lg text-sm text-foreground hover:bg-accent hover:text-accent-foreground transition-colors truncate"
                        >
                            {c}
                        </button>
                    ))
                ) : (
                    <div className="px-2.5 py-1.5 text-xs text-muted-foreground">
                        {candidates.length === 0 ? t('modelPickEmpty') : t('modelPickNoMatch')}
                    </div>
                )}
            </PopoverContent>
        </Popover>
    );
}

export function ChannelForm({
    formData,
    onFormDataChange,
    onSubmit,
    isPending,
    submitText,
    pendingText,
    onCancel,
    cancelText,
    idPrefix = 'channel',
}: ChannelFormProps) {
    const t = useTranslations('channel.form');

    // 按渠道类型展示 base_url 填写提示（各协议对版本路径的要求不同）
    const baseUrlHintKey: Record<ChannelType, string> = {
        [ChannelType.OpenAIChat]: 'baseUrlHintOpenAIChat',
        [ChannelType.OpenAIResponse]: 'baseUrlHintOpenAIResponse',
        [ChannelType.Anthropic]: 'baseUrlHintAnthropic',
        [ChannelType.Gemini]: 'baseUrlHintGemini',
        [ChannelType.Volcengine]: 'baseUrlHintVolcengine',
        [ChannelType.OpenAIEmbedding]: 'baseUrlHintOpenAIEmbedding',
        [ChannelType.Auto]: 'baseUrlHintAuto',
    };

    // Ensure the form always shows at least 1 row for base_urls / keys / custom_header.
    // This avoids "empty list" UI and also keeps URL + APIKEY layout consistent.
    useEffect(() => {
        if (!formData.base_urls || formData.base_urls.length === 0) {
            onFormDataChange({ ...formData, base_urls: [{ url: '', delay: 0 }] });
            return;
        }
        if (!formData.keys || formData.keys.length === 0) {
            onFormDataChange({ ...formData, keys: [{ enabled: true, channel_key: '' }] });
            return;
        }
        if (!formData.custom_header || formData.custom_header.length === 0) {
            onFormDataChange({ ...formData, custom_header: [{ header_key: '', header_value: '' }] });
        }
    }, [formData, onFormDataChange]);

    const autoModels = formData.model
        ? formData.model.split(',').map((m) => m.trim()).filter(Boolean)
        : [];
    const customModels = formData.custom_model
        ? formData.custom_model.split(',').map((m) => m.trim()).filter(Boolean)
        : [];
    const [inputValue, setInputValue] = useState('');
    const inputRef = useRef<HTMLInputElement>(null);

    const fetchModel = useFetchModel();
    const testChannel = useTestChannel();
    const [testResult, setTestResult] = useState<ChannelTestResult | null>(null);
    const [keyImportOpen, setKeyImportOpen] = useState(false);
    const [keyImportText, setKeyImportText] = useState('');

    const effectiveKey =
        formData.keys.find((k) => k.enabled && k.channel_key.trim())?.channel_key.trim() || '';

    const selectedModels = [...autoModels, ...customModels].filter(Boolean);
    const canTest = Boolean(
        formData.base_urls?.[0]?.url.trim() &&
        effectiveKey &&
        selectedModels.length > 0,
    );

    const updateModels = (nextAuto: string[], nextCustom: string[]) => {
        const model = nextAuto.join(',');
        const custom_model = nextCustom.join(',');
        if (formData.model === model && formData.custom_model === custom_model) return;
        onFormDataChange({ ...formData, model, custom_model });
    };

    const handleRefreshModels = async () => {
        if (!formData.base_urls?.[0]?.url || !effectiveKey) return;
        fetchModel.mutate(
            {
                type: formData.type,
                base_urls: formData.base_urls,
                keys: formData.keys
                    .filter((k) => k.channel_key.trim())
                    .map((k) => ({ enabled: k.enabled, channel_key: k.channel_key.trim() })),
                proxy_mode: formData.proxy_mode,
                proxy_config_id: formData.proxy_mode === 'pool' ? formData.proxy_config_id : null,
                match_regex: formData.match_regex.trim() || null,
                custom_header: formData.custom_header?.filter((h) => h.header_key.trim()) || [],
            },
            {
                onSuccess: (data) => {
                    if (data && data.length > 0) {
                        const nextAuto = Array.from(new Set([...autoModels, ...data].map((m) => m.trim()).filter(Boolean)));
                        updateModels(nextAuto, customModels);
                        toast.success(t('modelRefreshSuccess'));
                    } else {
                        toast.warning(t('modelRefreshEmpty'));
                    }
                },
                onError: (error) => {
                    const errorMessage = error instanceof Error ? error.message : String(error);
                    toast.error(t('modelRefreshFailed'), { description: errorMessage });
                },
            }
        );
    };

    const handleAddModel = (model: string) => {
        const trimmedModel = model.trim();
        if (trimmedModel && !customModels.includes(trimmedModel) && !autoModels.includes(trimmedModel)) {
            updateModels(autoModels, [...customModels, trimmedModel]);
        }
        setInputValue('');
    };

    const handleTest = () => {
        if (!canTest) return;
        setTestResult(null);
        testChannel.mutate(
            {
                type: formData.type,
                base_urls: (formData.base_urls ?? []).filter((u) => u.url.trim()).map((u) => ({
                    url: u.url.trim(),
                    delay: Number(u.delay || 0),
                })),
                keys: (formData.keys ?? [])
                    .filter((k) => k.channel_key.trim())
                    .map((k) => ({ enabled: k.enabled, channel_key: k.channel_key.trim() })),
                proxy_mode: formData.proxy_mode,
                proxy_config_id: formData.proxy_mode === 'pool' ? formData.proxy_config_id : null,
                custom_header: (formData.custom_header ?? []).filter((h) => h.header_key.trim()),
                param_override: formData.param_override.trim() || null,
                model: autoModels[0] ?? customModels[0] ?? '',
                custom_model: formData.custom_model,
                force_deep_seek_thinking: formData.force_deep_seek_thinking,
            },
            {
                onSuccess: (result) => {
                    setTestResult(result);
                },
                onError: (error) => {
                    const errorMessage = error instanceof Error ? error.message : String(error);
                    setTestResult({
                        success: false,
                        status_code: 0,
                        duration_ms: 0,
                        model: autoModels[0] ?? customModels[0] ?? '',
                        protocol: '',
                        error: errorMessage,
                    });
                },
            },
        );
    };

    const handleRemoveAutoModel = (model: string) => {
        updateModels(autoModels.filter(m => m !== model), customModels);
    };

    const handleRemoveCustomModel = (model: string) => {
        updateModels(autoModels, customModels.filter(m => m !== model));
    };

    const handleAddRedirect = () => {
        onFormDataChange({
            ...formData,
            model_redirects: [...(formData.model_redirects ?? []), { model: '', target_model: '' }],
        });
    };

    const handleUpdateRedirect = (idx: number, patch: Partial<ModelRedirect>) => {
        const next = (formData.model_redirects ?? []).map((r, i) => (i === idx ? { ...r, ...patch } : r));
        onFormDataChange({ ...formData, model_redirects: next });
    };

    const handleRemoveRedirect = (idx: number) => {
        const next = (formData.model_redirects ?? []).filter((_, i) => i !== idx);
        onFormDataChange({ ...formData, model_redirects: next });
    };

    const handleAddChannelGroup = () => {
        onFormDataChange({
            ...formData,
            channel_groups: [
                ...(formData.channel_groups ?? []),
                { alias: '', mode: GroupMode.Weighted, items: [{ model: '', priority: 0, weight: 1 }] },
            ],
        });
    };

    const handleUpdateChannelGroup = (idx: number, patch: Partial<ChannelGroup>) => {
        const next = (formData.channel_groups ?? []).map((g, i) => (i === idx ? { ...g, ...patch } : g));
        onFormDataChange({ ...formData, channel_groups: next });
    };

    const handleRemoveChannelGroup = (idx: number) => {
        const next = (formData.channel_groups ?? []).filter((_, i) => i !== idx);
        onFormDataChange({ ...formData, channel_groups: next });
    };

    const handleAddChannelGroupItem = (groupIdx: number) => {
        const next = (formData.channel_groups ?? []).map((g, i) =>
            i === groupIdx ? { ...g, items: [...(g.items ?? []), { model: '', priority: 0, weight: 1 }] } : g
        );
        onFormDataChange({ ...formData, channel_groups: next });
    };

    const handleUpdateChannelGroupItem = (groupIdx: number, itemIdx: number, patch: Partial<ChannelGroupItem>) => {
        const next = (formData.channel_groups ?? []).map((g, i) => {
            if (i !== groupIdx) return g;
            return {
                ...g,
                items: (g.items ?? []).map((it, j) => (j === itemIdx ? { ...it, ...patch } : it)),
            };
        });
        onFormDataChange({ ...formData, channel_groups: next });
    };

    const handleRemoveChannelGroupItem = (groupIdx: number, itemIdx: number) => {
        const next = (formData.channel_groups ?? []).map((g, i) =>
            i === groupIdx ? { ...g, items: (g.items ?? []).filter((_, j) => j !== itemIdx) } : g
        );
        onFormDataChange({ ...formData, channel_groups: next });
    };

    // 供"模型重定向上游模型"与"模型分组条目模型"使用的候选列表：
    // 已拉取的自动模型 + 手动添加模型；仍保留手动输入（候选列表外可自由填写）。
    const modelCandidates = selectedModels;

    const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            if (inputValue.trim()) handleAddModel(inputValue);
        }
    };

    const handleAddKey = () => {
        onFormDataChange({
            ...formData,
            keys: [...formData.keys, { enabled: true, channel_key: '' }],
        });
    };

    const appendImportedKeys = (imported: string[]) => {
        const current = formData.keys ?? [];
        const existing = new Set(current.map((k) => k.channel_key.trim()).filter(Boolean));
        const fresh = imported.filter((key) => !existing.has(key));
        const result = { added: fresh.length, duplicated: imported.length - fresh.length };
        if (fresh.length === 0) return result;

        let fillIndex = 0;
        const next = current.map((k) => {
            if (k.channel_key.trim() !== '' || fillIndex >= fresh.length) return k;
            return { ...k, channel_key: fresh[fillIndex++], enabled: true };
        });
        const remaining = fresh.slice(fillIndex).map((channel_key) => ({
            enabled: true,
            channel_key,
            remark: '',
        }));

        onFormDataChange({ ...formData, keys: [...next, ...remaining] });
        return result;
    };

    const closeKeyImport = () => {
        setKeyImportOpen(false);
        setKeyImportText('');
    };

    const handleKeyImport = () => {
        const content = keyImportText.trim();
        if (!content) {
            toast.warning(t('batchImportEmpty'));
            return;
        }

        // 导入结果先合并到草稿 Key 列表；新建渠道在创建时保存，
        // 已保存渠道由表单统一的 Save 动作随 keys_to_add 一起提交。
        const parsed = parseChannelKeyImportContent(content);
        if (parsed.keys.length === 0) {
            toast.warning(t('batchImportEmpty'));
            return;
        }
        const { added, duplicated: existingDuplicates } = appendImportedKeys(parsed.keys);
        if (added > 0) {
            toast.success(t('batchImportSuccess', { count: added }));
            closeKeyImport();
        }
        const totalDuplicates = parsed.duplicated + existingDuplicates;
        if (totalDuplicates > 0) {
            toast.info(t('batchImportDuplicates', { count: totalDuplicates }));
        }
    };

    const handleUpdateKey = (idx: number, patch: Partial<ChannelKeyFormItem>) => {
        const next = formData.keys.map((k, i) => (i === idx ? { ...k, ...patch } : k));
        onFormDataChange({ ...formData, keys: next });
    };

    const handleRemoveKey = (idx: number) => {
        const curr = formData.keys ?? [];
        if (curr.length <= 1) return;
        const next = curr.filter((_, i) => i !== idx);
        onFormDataChange({ ...formData, keys: next });
    };

    const handleAddBaseUrl = () => {
        onFormDataChange({
            ...formData,
            base_urls: [...(formData.base_urls ?? []), { url: '', delay: 0 }],
        });
    };

    const handleUpdateBaseUrl = (idx: number, patch: Partial<Channel['base_urls'][number]>) => {
        const next = (formData.base_urls ?? []).map((u, i) => (i === idx ? { ...u, ...patch } : u));
        onFormDataChange({ ...formData, base_urls: next });
    };

    const handleRemoveBaseUrl = (idx: number) => {
        const curr = formData.base_urls ?? [];
        if (curr.length <= 1) return;
        onFormDataChange({ ...formData, base_urls: curr.filter((_, i) => i !== idx) });
    };

    const handleAddHeader = () => {
        onFormDataChange({
            ...formData,
            custom_header: [...(formData.custom_header ?? []), { header_key: '', header_value: '' }],
        });
    };

    const handleUpdateHeader = (idx: number, patch: Partial<Channel['custom_header'][number]>) => {
        const next = (formData.custom_header ?? []).map((h, i) => (i === idx ? { ...h, ...patch } : h));
        onFormDataChange({ ...formData, custom_header: next });
    };

    const handleRemoveHeader = (idx: number) => {
        const curr = formData.custom_header ?? [];
        if (curr.length <= 1) return;
        onFormDataChange({ ...formData, custom_header: curr.filter((_, i) => i !== idx) });
    };

    return (
        <form onSubmit={onSubmit} className="space-y-4 px-1">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-2">
                    <label htmlFor={`${idPrefix}-name`} className="text-sm font-medium text-card-foreground">
                        {t('name')}
                    </label>
                    <Input
                        className='rounded-xl'
                        id={`${idPrefix}-name`}
                        type="text"
                        value={formData.name}
                        onChange={(event) => onFormDataChange({ ...formData, name: event.target.value })}
                        required
                    />
                </div>

                <div className="space-y-2">
                    <label htmlFor={`${idPrefix}-type`} className="text-sm font-medium text-card-foreground">
                        {t('type')}
                    </label>
                    <Select
                        value={String(formData.type)}
                        onValueChange={(value) => onFormDataChange({ ...formData, type: Number(value) as ChannelType })}
                    >
                        <SelectTrigger id={`${idPrefix}-type`} className="rounded-xl w-full border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                            <SelectValue />
                        </SelectTrigger>
                        <SelectContent className='rounded-xl'>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIChat)}>{t('typeOpenAIChat')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIResponse)}>{t('typeOpenAIResponse')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Anthropic)}>{t('typeAnthropic')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Gemini)}>{t('typeGemini')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.OpenAIEmbedding)}>{t('typeOpenAIEmbedding')}</SelectItem>
                            <SelectItem className='rounded-xl' value={String(ChannelType.Auto)}>{t('typeAuto')}</SelectItem>
                        </SelectContent>
                    </Select>
                </div>
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between">
                    <label className="text-sm font-medium text-card-foreground">
                        {t('baseUrls')} {formData.base_urls.length > 0 ? `(${formData.base_urls.length})` : ''}
                    </label>
                    <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={handleAddBaseUrl}
                        className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                    >
                        <Plus className="h-3 w-3 mr-1" />
                        {t('add')}
                    </Button>
                </div>
                <p className="text-xs text-muted-foreground/80 leading-relaxed">
                    {t(baseUrlHintKey[formData.type])}
                </p>
                <div className="space-y-2">
                    {(formData.base_urls ?? []).map((u, idx) => (
                        <div key={`baseurl-${idx}`} className="flex items-center gap-2">
                            <Input
                                id={`${idPrefix}-base-${idx}`}
                                type="url"
                                value={u.url}
                                onChange={(e) => handleUpdateBaseUrl(idx, { url: e.target.value })}
                                placeholder={t('baseUrlUrl')}
                                required={idx === 0}
                                className="rounded-xl flex-1"
                            />
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => handleRemoveBaseUrl(idx)}
                                disabled={(formData.base_urls ?? []).length <= 1}
                                className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive disabled:opacity-40 hover:bg-transparent"
                                title="Remove"
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                    ))}
                </div>
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between">
                    <label className="text-sm font-medium text-card-foreground">
                        {t('apiKey')} {formData.keys.length > 0 ? `(${formData.keys.length})` : ''}
                    </label>
                    <div className="flex items-center gap-1">
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => setKeyImportOpen((open) => !open)}
                            className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                        >
                            <Import className="h-3 w-3 mr-1" />
                            {t('batchImport')}
                        </Button>
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={handleAddKey}
                            className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                        >
                            <Plus className="h-3 w-3 mr-1" />
                            {t('add')}
                        </Button>
                    </div>
                </div>
                {keyImportOpen ? (
                    <div className="rounded-xl border border-border bg-muted/30 p-3 space-y-2">
                        <textarea
                            value={keyImportText}
                            onChange={(e) => setKeyImportText(e.target.value)}
                            placeholder={t('batchImportPlaceholder')}
                            rows={6}
                            className="w-full rounded-xl border border-border bg-background px-3 py-2 text-sm font-mono text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
                        />
                        <div className="flex flex-wrap items-center justify-between gap-2">
                            <p className="text-xs text-muted-foreground/80 leading-relaxed">
                                {t('batchImportHint')}
                            </p>
                            <div className="flex items-center gap-2">
                                <Button
                                    type="button"
                                    variant="outline"
                                    size="sm"
                                    onClick={closeKeyImport}
                                    className="rounded-xl"
                                >
                                    {t('batchImportCancel')}
                                </Button>
                                <Button
                                    type="button"
                                    size="sm"
                                    onClick={handleKeyImport}
                                    disabled={!keyImportText.trim()}
                                    className="rounded-xl"
                                >
                                    <Import className="h-3 w-3 mr-1" />
                                    {t('batchImportConfirm')}
                                </Button>
                            </div>
                        </div>
                    </div>
                ) : null}
                <div className="space-y-2">
                    {(formData.keys ?? []).map((k, idx) => (
                        <div key={k.id ?? `new-${idx}`} className="flex items-center gap-2">
                            <Input
                                type="text"
                                value={k.channel_key}
                                onChange={(e) => handleUpdateKey(idx, { channel_key: e.target.value })}
                                placeholder={t('apiKey')}
                                required={idx === 0}
                                className="rounded-xl flex-1"
                            />
                            <Input
                                type="text"
                                value={k.remark ?? ''}
                                onChange={(e) => handleUpdateKey(idx, { remark: e.target.value })}
                                placeholder={t('remark')}
                                className="rounded-xl w-32"
                            />
                            <Switch
                                checked={k.enabled}
                                onCheckedChange={(checked) => handleUpdateKey(idx, { enabled: checked })}
                            />
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => handleRemoveKey(idx)}
                                disabled={(formData.keys ?? []).length <= 1}
                                className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent disabled:opacity-40"
                                title="Remove"
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                    ))}
                </div>
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                    <label className="text-sm font-medium text-card-foreground">{t('model')}</label>
                    <div className="flex items-center gap-1">
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={handleTest}
                            disabled={!canTest || testChannel.isPending}
                            title={canTest ? t('testHint') : t('testDisabledHint')}
                            className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent disabled:opacity-50"
                        >
                            {testChannel.isPending
                                ? <Loader2 className="h-3 w-3 mr-1 animate-spin" />
                                : <FlaskConical className="h-3 w-3 mr-1" />}
                            {testChannel.isPending ? t('testing') : t('test')}
                        </Button>
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={handleRefreshModels}
                            disabled={!formData.base_urls?.[0]?.url || !effectiveKey || fetchModel.isPending}
                            className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                        >
                            <RefreshCw className={`h-3 w-3 mr-1 ${fetchModel.isPending ? 'animate-spin' : ''}`} />
                            {t('modelRefresh')}
                        </Button>
                    </div>
                </div>
                <input type="hidden" value={formData.model} required />

                <div className="relative">
                    <Input
                        ref={inputRef}
                        id={`${idPrefix}-model-custom`}
                        type="text"
                        value={inputValue}
                        onChange={(e) => setInputValue(e.target.value)}
                        onKeyDown={handleInputKeyDown}
                        placeholder={t('modelCustomPlaceholder')}
                        className="pr-10 rounded-xl"
                    />
                    {inputValue.trim() && !customModels.includes(inputValue.trim()) && !autoModels.includes(inputValue.trim()) && (
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => handleAddModel(inputValue)}
                            className="absolute rounded-lg right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0 text-muted-foreground hover:bg-accent hover:text-accent-foreground transition-colors"
                            title={t('modelAdd')}
                        >
                            <Plus className="size-4" />
                        </Button>
                    )}
                </div>

                <div className="space-y-2">
                    <div className="flex items-center justify-between">
                        <label className="text-xs font-medium text-card-foreground">
                            {t('modelSelected')} {(autoModels.length + customModels.length) > 0 && `(${autoModels.length + customModels.length})`}
                        </label>
                        {(autoModels.length + customModels.length) > 0 && (
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => {
                                    updateModels([], []);
                                }}
                                className="h-6 px-2 text-xs text-muted-foreground/50 hover:text-muted-foreground hover:bg-transparent"
                            >
                                {t('modelClearAll')}
                            </Button>
                        )}
                    </div>
                    <div className="rounded-xl border border-border bg-muted/30 p-2.5 max-h-40 min-h-12 overflow-y-auto">
                        {(autoModels.length + customModels.length) > 0 ? (
                            <div className="flex flex-wrap gap-1.5">
                                {autoModels.map((model) => (
                                    <Badge key={model} variant="secondary" className="bg-muted hover:bg-muted/80">
                                        {model}
                                        <button
                                            type="button"
                                            onClick={() => handleRemoveAutoModel(model)}
                                            className="ml-1 rounded-sm opacity-70 hover:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
                                        >
                                            <X className="h-3 w-3" />
                                        </button>
                                    </Badge>
                                ))}
                                {customModels.map((model) => (
                                    <Badge key={model} className="bg-primary hover:bg-primary/90">
                                        {model}
                                        <button
                                            type="button"
                                            onClick={() => handleRemoveCustomModel(model)}
                                            className="ml-1 rounded-sm opacity-70 hover:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
                                        >
                                            <X className="h-3 w-3" />
                                        </button>
                                    </Badge>
                                ))}
                            </div>
                        ) : (
                            <div className="flex items-center justify-center h-8 text-xs text-muted-foreground">
                                {t('modelNoSelected')}
                            </div>
                        )}
                    </div>
                </div>
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                    <label className="text-sm font-medium text-card-foreground">{t('modelRedirect')}</label>
                    <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={handleAddRedirect}
                        className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                    >
                        <Plus className="h-3 w-3 mr-1" />
                        {t('modelRedirectAdd')}
                    </Button>
                </div>
                <p className="text-xs text-muted-foreground/80 leading-relaxed">{t('modelRedirectHint')}</p>
                {(formData.model_redirects ?? []).length > 0 ? (
                    <div className="space-y-2">
                        {(formData.model_redirects ?? []).map((redirect, idx) => (
                            <div key={`redirect-${idx}`} className="flex items-center gap-2">
                                <Input
                                    type="text"
                                    value={redirect.model}
                                    onChange={(e) => handleUpdateRedirect(idx, { model: e.target.value })}
                                    placeholder={t('modelRedirectAlias')}
                                    className="rounded-xl flex-1"
                                />
                                <span className="text-xs text-muted-foreground shrink-0">→</span>
                                <ModelCombobox
                                    value={redirect.target_model}
                                    onChange={(value) => handleUpdateRedirect(idx, { target_model: value })}
                                    candidates={modelCandidates}
                                    placeholder={t('modelRedirectTarget')}
                                />
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="sm"
                                    onClick={() => handleRemoveRedirect(idx)}
                                    className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                    title={t('modelRedirectRemove')}
                                >
                                    <X className="h-4 w-4" />
                                </Button>
                            </div>
                        ))}
                    </div>
                ) : null}
                <label className="flex items-center gap-2 cursor-pointer">
                    <Switch
                        checked={formData.model_redirect_only ?? false}
                        onCheckedChange={(checked) => onFormDataChange({ ...formData, model_redirect_only: checked })}
                    />
                    <span className="text-sm text-card-foreground">{t('modelRedirectOnly')}</span>
                </label>
                <p className="text-xs text-muted-foreground/80 leading-relaxed">{t('modelRedirectOnlyHint')}</p>
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                    <label className="text-sm font-medium text-card-foreground">{t('channelGroups')}</label>
                    <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={handleAddChannelGroup}
                        className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                    >
                        <Plus className="h-3 w-3 mr-1" />
                        {t('channelGroupAdd')}
                    </Button>
                </div>
                <p className="text-xs text-muted-foreground/80 leading-relaxed">{t('channelGroupHint')}</p>
                {(formData.channel_groups ?? []).map((group, groupIdx) => (
                    <div key={`channel-group-${groupIdx}`} className="space-y-2 rounded-xl border border-border/60 bg-muted/30 p-2.5">
                        <div className="flex items-center gap-2">
                            <Input
                                type="text"
                                value={group.alias}
                                onChange={(e) => handleUpdateChannelGroup(groupIdx, { alias: e.target.value })}
                                placeholder={t('channelGroupAlias')}
                                className="rounded-xl flex-1"
                            />
                            <Select
                                value={String(group.mode)}
                                onValueChange={(value) =>
                                    handleUpdateChannelGroup(groupIdx, { mode: Number(value) as ChannelGroup['mode'] })
                                }
                            >
                                <SelectTrigger id={`${idPrefix}-channel-group-mode-${groupIdx}`} className="rounded-xl w-32 shrink-0 border border-border px-3 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                                    <SelectValue />
                                </SelectTrigger>
                                <SelectContent className='rounded-xl'>
                                    <SelectItem className='rounded-xl' value={String(GroupMode.Weighted)}>{t('channelGroupModeWeighted')}</SelectItem>
                                    <SelectItem className='rounded-xl' value={String(GroupMode.Failover)}>{t('channelGroupModeFailover')}</SelectItem>
                                </SelectContent>
                            </Select>
                            <Button
                                type="button"
                                variant="ghost"
                                size="sm"
                                onClick={() => handleRemoveChannelGroup(groupIdx)}
                                className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                title={t('channelGroupRemove')}
                            >
                                <X className="h-4 w-4" />
                            </Button>
                        </div>
                        <div className="space-y-1.5">
                            {(group.items ?? []).map((item, itemIdx) => (
                                <div key={`channel-group-${groupIdx}-item-${itemIdx}`} className="flex items-center gap-2">
                                    <ModelCombobox
                                        value={item.model}
                                        onChange={(value) => handleUpdateChannelGroupItem(groupIdx, itemIdx, { model: value })}
                                        candidates={modelCandidates}
                                        placeholder={t('channelGroupModel')}
                                    />
                                    <Input
                                        type="number"
                                        min={1}
                                        value={item.weight}
                                        onChange={(e) =>
                                            handleUpdateChannelGroupItem(groupIdx, itemIdx, {
                                                weight: Math.max(1, parseInt(e.target.value) || 1),
                                            })
                                        }
                                        placeholder={t('channelGroupWeight')}
                                        title={t('channelGroupWeight')}
                                        className="rounded-xl w-16 shrink-0"
                                    />
                                    <Input
                                        type="number"
                                        min={0}
                                        value={item.priority}
                                        onChange={(e) =>
                                            handleUpdateChannelGroupItem(groupIdx, itemIdx, {
                                                priority: Math.max(0, parseInt(e.target.value) || 0),
                                            })
                                        }
                                        placeholder={t('channelGroupPriority')}
                                        title={t('channelGroupPriority')}
                                        className="rounded-xl w-16 shrink-0"
                                    />
                                    <Button
                                        type="button"
                                        variant="ghost"
                                        size="sm"
                                        onClick={() => handleRemoveChannelGroupItem(groupIdx, itemIdx)}
                                        className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent"
                                        title={t('channelGroupRemoveItem')}
                                    >
                                        <X className="h-4 w-4" />
                                    </Button>
                                </div>
                            ))}
                        </div>
                        <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => handleAddChannelGroupItem(groupIdx)}
                            className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                        >
                            <Plus className="h-3 w-3 mr-1" />
                            {t('channelGroupAddItem')}
                        </Button>
                    </div>
                ))}
            </div>

            <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                    <label className="text-sm font-medium text-card-foreground">{t('tags')}</label>
                </div>
                <TagInput
                    value={formData.tags ?? []}
                    onChange={(tags) => onFormDataChange({ ...formData, tags })}
                    placeholder={t('tagsPlaceholder')}
                />
            </div>

            <div className="rounded-xl border bg-card p-4">
                <ProxySelector
                    value={{ proxy_mode: formData.proxy_mode, proxy_config_id: formData.proxy_config_id }}
                    onChange={(next) => onFormDataChange({
                        ...formData,
                        proxy_mode: next.proxy_mode as Channel['proxy_mode'],
                        proxy_config_id: next.proxy_config_id ?? null,
                    })}
                />
            </div>

            <Accordion type="single" collapsible className="w-full border rounded-xl bg-card">
                <AccordionItem value="advanced" className="border-none">
                    <AccordionTrigger className="text-sm font-medium text-card-foreground py-3 px-4 hover:no-underline hover:bg-muted/30 rounded-xl transition-colors">
                        {t('advanced')}
                    </AccordionTrigger>
                    <AccordionContent className="pt-4 px-4 pb-4 space-y-4 border-t">
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                            {formData.type === ChannelType.OpenAIResponse ? (
                                <div className="space-y-2">
                                    <label htmlFor={`${idPrefix}-ws-mode`} className="text-sm font-medium text-card-foreground">
                                        {t('wsMode')}
                                    </label>
                                    <Select
                                        value={formData.ws_mode ?? 'inherit'}
                                        onValueChange={(value) => onFormDataChange({ ...formData, ws_mode: value as ChannelWSMode })}
                                    >
                                        <SelectTrigger id={`${idPrefix}-ws-mode`} className="rounded-xl w-full border border-border px-4 py-2 text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                                            <SelectValue />
                                        </SelectTrigger>
                                        <SelectContent className='rounded-xl'>
                                            <SelectItem className='rounded-xl' value="inherit">{t('wsModeInherit')}</SelectItem>
                                            <SelectItem className='rounded-xl' value="passthrough">{t('wsModePassthrough')}</SelectItem>
                                            <SelectItem className='rounded-xl' value="transform">{t('wsModeTransform')}</SelectItem>
                                            <SelectItem className='rounded-xl' value="off">{t('wsModeOff')}</SelectItem>
                                        </SelectContent>
                                    </Select>
                                </div>
                            ) : null}

                        </div>

                        <div className="space-y-2">
                            <div className="flex items-center justify-between">
                                <label className="text-sm font-medium text-card-foreground">
                                    {t('customHeader')} {formData.custom_header.length > 0 ? `(${formData.custom_header.length})` : ''}
                                </label>
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="sm"
                                    onClick={handleAddHeader}
                                    className="h-6 px-2 text-xs text-muted-foreground/70 hover:text-muted-foreground hover:bg-transparent"
                                >
                                    <Plus className="h-3 w-3 mr-1" />
                                    {t('customHeaderAdd')}
                                </Button>
                            </div>
                            <div className="space-y-2">
                                {(formData.custom_header ?? []).map((h, idx) => (
                                    <div key={`hdr-${idx}`} className="flex items-center gap-2">
                                        <Input
                                            type="text"
                                            value={h.header_key}
                                            onChange={(e) => handleUpdateHeader(idx, { header_key: e.target.value })}
                                            placeholder={t('customHeaderKey')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Input
                                            type="text"
                                            value={h.header_value}
                                            onChange={(e) => handleUpdateHeader(idx, { header_value: e.target.value })}
                                            placeholder={t('customHeaderValue')}
                                            className="rounded-xl flex-1"
                                        />
                                        <Button
                                            type="button"
                                            variant="ghost"
                                            size="sm"
                                            onClick={() => handleRemoveHeader(idx)}
                                            disabled={(formData.custom_header ?? []).length <= 1}
                                            className="h-8 w-8 p-0 rounded-xl text-muted-foreground hover:text-destructive hover:bg-transparent disabled:opacity-40"
                                            title="Remove"
                                        >
                                            <X className="h-4 w-4" />
                                        </Button>
                                    </div>
                                ))}
                            </div>
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-match-regex`} className="text-sm font-medium text-card-foreground">
                                {t('matchRegex')}
                            </label>
                            <Input
                                id={`${idPrefix}-match-regex`}
                                type="text"
                                value={formData.match_regex}
                                onChange={(e) => onFormDataChange({ ...formData, match_regex: e.target.value })}
                                placeholder={t('matchRegexPlaceholder')}
                                className="rounded-xl"
                            />
                        </div>

                        <div className="space-y-2">
                            <label htmlFor={`${idPrefix}-param-override`} className="text-sm font-medium text-card-foreground">
                                {t('paramOverride')}
                            </label>
                            <textarea
                                id={`${idPrefix}-param-override`}
                                value={formData.param_override}
                                onChange={(e) => onFormDataChange({ ...formData, param_override: e.target.value })}
                                placeholder={t('paramOverridePlaceholder')}
                                className="min-h-28 w-full rounded-xl border border-border bg-background px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            />
                        </div>
                    </AccordionContent>
                </AccordionItem>
            </Accordion>

            <div className="flex flex-wrap items-center justify-between gap-4 p-4 rounded-xl bg-muted/20 border border-border/50">
                <label className="flex items-center gap-2 cursor-pointer">
                    <Switch
                        checked={formData.enabled}
                        onCheckedChange={(checked) => onFormDataChange({ ...formData, enabled: checked })}
                    />
                    <span className="text-sm font-medium text-card-foreground">{t('enabled')}</span>
                </label>
                <div className="flex flex-wrap items-center gap-6">
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.auto_sync}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, auto_sync: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('autoSync')}</span>
                    </label>
                    <label className="flex items-center gap-2 cursor-pointer">
                        <Switch
                            checked={formData.force_deep_seek_thinking}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, force_deep_seek_thinking: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('forceDeepSeekThinking')}</span>
                    </label>
                    <label className="flex items-center gap-2 cursor-pointer" title={t('probeEnabledHint')}>
                        <Switch
                            checked={formData.probe_enabled}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, probe_enabled: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('probeEnabled')}</span>
                    </label>
                    <label className="flex items-center gap-2 cursor-pointer" title={t('schedulingExemptHint')}>
                        <Switch
                            checked={formData.scheduling_exempt}
                            onCheckedChange={(checked) => onFormDataChange({ ...formData, scheduling_exempt: checked })}
                        />
                        <span className="text-sm text-card-foreground">{t('schedulingExempt')}</span>
                    </label>
                </div>
            </div>

            <div className={`flex flex-col gap-3 pt-2 ${onCancel ? 'sm:flex-row' : ''}`}>
                {onCancel && cancelText && (
                    <Button
                        type="button"
                        variant="secondary"
                        onClick={onCancel}
                        className="w-full sm:flex-1 rounded-2xl h-12"
                    >
                        {cancelText}
                    </Button>
                )}
                <Button
                    type="submit"
                    disabled={isPending}
                    className="w-full sm:flex-1 rounded-2xl h-12"
                >
                    {isPending ? pendingText : submitText}
                </Button>
            </div>

            {testResult ? (
                <div
                    className={`rounded-xl border p-4 space-y-2 ${
                        testResult.success
                            ? 'border-emerald-500/30 bg-emerald-500/5'
                            : 'border-destructive/30 bg-destructive/5'
                    }`}
                >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                        <span className={`text-sm font-semibold ${testResult.success ? 'text-emerald-700 dark:text-emerald-400' : 'text-destructive'}`}>
                            {testResult.success ? t('testSuccess') : t('testFailed')}
                        </span>
                        <span className="text-xs text-muted-foreground">
                            {testResult.status_code > 0 ? `HTTP ${testResult.status_code}` : ''}
                            {testResult.duration_ms > 0 ? ` · ${testResult.duration_ms}ms` : ''}
                        </span>
                    </div>
                    {testResult.model ? (
                        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                            <span>{t('testModel')}: {testResult.model}</span>
                            {testResult.protocol ? <span>{t('testProtocol')}: {testResult.protocol}</span> : null}
                        </div>
                    ) : null}
                    {testResult.success && testResult.output ? (
                        <div className="space-y-1">
                            <div className="text-xs font-medium text-muted-foreground">{t('testOutput')}</div>
                            <pre className="whitespace-pre-wrap break-words rounded-lg bg-muted/40 p-3 text-xs text-card-foreground max-h-40 overflow-y-auto">
                                {testResult.output}
                            </pre>
                        </div>
                    ) : null}
                    {!testResult.success && testResult.error ? (
                        <div className="space-y-1">
                            <div className="text-xs font-medium text-muted-foreground">{t('testError')}</div>
                            <pre className="whitespace-pre-wrap break-words rounded-lg bg-destructive/5 p-3 text-xs text-destructive max-h-40 overflow-y-auto">
                                {testResult.error}
                            </pre>
                        </div>
                    ) : null}
                    {testResult.usage ? (
                        <div className="text-xs text-muted-foreground">
                            {t('testUsage')}: {testResult.usage.prompt_tokens} / {testResult.usage.completion_tokens} / {testResult.usage.total_tokens}
                        </div>
                    ) : null}
                </div>
            ) : null}
        </form>
    );
}