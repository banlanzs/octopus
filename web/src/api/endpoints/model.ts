import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiClient } from '../client';
import { logger } from '@/lib/logger';

/**
 * LLM 价格信息
 */
export interface LLMPrice {
    input: number;
    output: number;
    cache_read: number;
    cache_write: number;
}

/**
 * LLM 模型信息
 */
export interface LLMInfo extends LLMPrice {
    name: string;
}

/**
 * LLM 渠道关联信息
 */
export interface LLMChannel {
    name: string;
    enabled: boolean;
    channel_id: number;
    channel_name: string;
    site_id?: number | null;
    site_account_id?: number | null;
    site_group_key?: string;
    site_group_name?: string;
    site_name?: string;
    site_account_name?: string;
    endpoint_type?: string;
    /**
     * 该 (渠道, 模型) 是否配置了专属渠道价（true 表示计费不走全局兜底）。
     */
    has_channel_price?: boolean;
    /**
     * 该 (渠道, 模型) 的计费价格（已乘渠道倍率，供价格页直接渲染展示）。
     * 由后端在 /api/v1/model/channel 附带。
     */
    price?: LLMPrice;
    /**
     * 该渠道的计费倍率（默认 1）。最终单价 = 模型价格 × 倍率。
     */
    price_multiplier?: number;
    /**
     * 自动排序健康度摘要（AutoRank 被动统计 + 渠道级熔断状态）。
     * 有采样或存在熔断/降级信号时由后端附带，用于解释自动排序的流量分配。
     */
    auto_rank?: AutoRankHealth;
}

/**
 * 渠道内模型价格更新请求体
 */
export interface ChannelModelPricePayload {
    channel_id: number;
    model_name: string;
    input?: number;
    output?: number;
    cache_read?: number;
    cache_write?: number;
}

/**
 * 渠道-模型的自动排序健康度摘要（对应后端 model.LLMAutoRankHealth）
 */
export interface AutoRankHealth {
    /** 时间窗口内采样请求数 */
    samples: number;
    /** samples 中来自主动探测的条数：只补成功率，不计延迟、不推进样本充足判定 */
    probe_samples: number;
    /** 只有探测样本且探测全失败：已从探索池剔除，仅留在 failover 链末尾兜底 */
    probe_dead: boolean;
    /** 窗口内失败请求数 */
    failures: number;
    /** 窗口成功率 0~1 */
    success_rate: number;
    /** Wilson 成功率置信下界 0~1 */
    success_confidence: number;
    /** EWMA 平滑延迟（毫秒） */
    ewma_latency_ms: number;
    /** EWMA 首 Token 延迟（毫秒） */
    ewma_ttfb_ms: number;
    /** 基础排序得分：成功率置信下界*100 - 延迟(秒) */
    score: number;
    /** 应用渠道聚合与相对 TTFB 修正后的分数 */
    effective_score: number;
    /** 当前动态优先列表排名（1-based） */
    rank: number;
    /** 采样档位：0 冷启动、1 欠采样、2 样本充分 */
    tier: number;
    /** 公平调度目标占比 0~1 */
    target_share: number;
    /** 进程内实际转发占比 0~1 */
    actual_share: number;
    last_sample_at: string;
    last_dispatched_at: string;
    selection_reason: string;
    /** 渠道是否处于聚合惩罚（多模型同时恶化，得分被统一压低） */
    degraded: boolean;
    /** 渠道级熔断是否生效 */
    channel_tripped: boolean;
    /** 渠道级熔断剩余冷却秒数 */
    channel_cooldown_sec: number;
    /** 渠道级累计熔断次数 */
    channel_trip_count: number;
    /** 窗口样本时间线摘要（✓ 成功 / ✗ 失败 / p 探测，从旧到新）；无样本时缺省 */
    trail_summary?: string;
}

/**
 * 获取 LLM 模型列表 Hook
 * 
 * @example
 * const { data: models, isLoading, error } = useModelList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * models?.forEach(model => console.log(model.name, model.input));
 */
export function useModelList() {
    return useQuery({
        queryKey: ['models', 'list'],
        queryFn: async () => {
            return apiClient.get<LLMInfo[]>('/api/v1/model/list');
        },
        refetchInterval: 30000,
    });
}

/**
 * 获取 LLM 模型与渠道关联列表 Hook
 * 
 * @example
 * const { data: channelModels, isLoading, error } = useModelChannelList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * channelModels?.forEach(item => console.log(item.name, item.channel_name));
 */
export function useModelChannelList(refetchIntervalMs = 30000) {
    return useQuery({
        queryKey: ['models', 'channel'],
        queryFn: async () => {
            return apiClient.get<LLMChannel[]>('/api/v1/model/channel');
        },
        refetchInterval: refetchIntervalMs,
    });
}

/**
 * 更新 LLM 模型 Hook
 * 
 * @example
 * const updateModel = useUpdateModel();
 * 
 * updateModel.mutate({
 *   name: 'gpt-4',
 *   input: 0.03,
 *   output: 0.06,
 *   cache_read: 0.015,
 *   cache_write: 0.03,
 * });
 */
export function useUpdateModel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: LLMInfo) => {
            return apiClient.post<LLMInfo>('/api/v1/model/update', data);
        },
        onSuccess: (data) => {
            logger.log('模型更新成功:', data);
            queryClient.invalidateQueries({ queryKey: ['models', 'list'] });
        },
        onError: (error) => {
            logger.error('模型更新失败:', error);
        },
    });
}

/**
 * 创建 LLM 模型 Hook
 * 
 * @example
 * const createModel = useCreateModel();
 * 
 * createModel.mutate({
 *   name: 'gpt-4',
 *   input: 0.03,
 *   output: 0.06,
 *   cache_read: 0.015,
 *   cache_write: 0.03,
 * });
 */
export function useCreateModel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: LLMInfo) => {
            return apiClient.post<LLMInfo>('/api/v1/model/create', data);
        },
        onSuccess: (data) => {
            logger.log('模型创建成功:', data);
            queryClient.invalidateQueries({ queryKey: ['models', 'list'] });
        },
        onError: (error) => {
            logger.error('模型创建失败:', error);
        },
    });
}

/**
 * 删除 LLM 模型 Hook
 * 
 * @example
 * const deleteModel = useDeleteModel();
 * 
 * deleteModel.mutate('gpt-4'); // 删除名称为 'gpt-4' 的模型
 */
export function useDeleteModel() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (name: string) => {
            return apiClient.post<null>('/api/v1/model/delete', { name });
        },
        onSuccess: () => {
            logger.log('模型删除成功');
            queryClient.invalidateQueries({ queryKey: ['models', 'list'] });
        },
        onError: (error) => {
            logger.error('模型删除失败:', error);
        },
    });
}

/**
 * 更新 LLM 模型价格 Hook
 * 
 * @example
 * const updatePrice = useUpdateModelPrice();
 * 
 * updatePrice.mutate(); // 触发价格更新
 */
export function useUpdateModelPrice() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async () => {
            return apiClient.post<null>('/api/v1/model/update-price', {});
        },
        onSuccess: () => {
            logger.log('模型价格更新成功');
            queryClient.invalidateQueries({ queryKey: ['models', 'last-update-time'] });
        },
        onError: (error) => {
            logger.error('模型价格更新失败:', error);
        },
    });
}

/**
 * 获取 LLM 模型价格最后更新时间 Hook
 * 
 * @example
 * const { data: lastUpdateTime } = useLastUpdateTime();
 * 
 * if (lastUpdateTime) {
 *   console.log('最后更新:', new Date(lastUpdateTime).toLocaleString());
 * }
 */
export function useLastUpdateTime() {
    return useQuery({
        queryKey: ['models', 'last-update-time'],
        queryFn: async () => {
            return apiClient.get<string>('/api/v1/model/last-update-time');
        },
        refetchInterval: 30000,
    });
}

/**
 * 更新渠道内模型价格 Hook
 *
 * @example
 * const updatePrice = useUpdateChannelModelPrice();
 *
 * updatePrice.mutate({
 *   channel_id: 1,
 *   model_name: 'gpt-4o',
 *   input: 0.03,
 *   output: 0.06,
 * });
 */
export function useUpdateChannelModelPrice() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: ChannelModelPricePayload) => {
            return apiClient.post<null>('/api/v1/model/channel-price/update', data);
        },
        onSuccess: () => {
            logger.log('渠道模型价格更新成功');
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道模型价格更新失败:', error);
        },
    });
}

/**
 * 删除渠道内模型价格 Hook（删除后计费回退到全局价）
 *
 * @example
 * const deletePrice = useDeleteChannelModelPrice();
 *
 * deletePrice.mutate({ channel_id: 1, model_name: 'gpt-4o' });
 */
export function useDeleteChannelModelPrice() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (data: { channel_id: number; model_name: string }) => {
            return apiClient.post<null>('/api/v1/model/channel-price/delete', data);
        },
        onSuccess: () => {
            logger.log('渠道模型价格已删除');
            queryClient.invalidateQueries({ queryKey: ['models', 'channel'] });
        },
        onError: (error) => {
            logger.error('渠道模型价格删除失败:', error);
        },
    });
}
