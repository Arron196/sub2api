<template>
  <div class="space-y-6">
    <section class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.telegram.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.telegram.description") }}
        </p>
      </div>

      <div class="space-y-5 p-6">
        <div v-if="loading" class="flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400">
          <span class="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-b-primary-600 dark:border-dark-600"></span>
          {{ t("common.loading") }}
        </div>

        <template v-else-if="config || status">
          <div class="space-y-4 border-b border-gray-100 pb-5 dark:border-dark-700">
            <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <h3 class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t("admin.settings.telegram.configuration.title") }}
                </h3>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.telegram.configuration.description") }}
                </p>
              </div>
              <div class="flex items-center gap-3">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-200">
                  {{ enabled ? t("admin.settings.telegram.configuration.enabled") : t("admin.settings.telegram.configuration.disabled") }}
                </span>
                <Toggle v-model="enabled" />
              </div>
            </div>

            <div class="grid gap-4 lg:grid-cols-2">
              <div class="space-y-1.5">
                <Input
                  id="telegram-bot-token"
                  v-model="botToken"
                  type="password"
                  autocomplete="new-password"
                  :label="t('admin.settings.telegram.configuration.token')"
                  :placeholder="t('admin.settings.telegram.configuration.tokenPlaceholder')"
                />
                <p class="text-sm text-gray-500 dark:text-gray-400">
                  {{ tokenStatusText }}
                </p>
                <p v-if="config?.token_configured" class="text-sm text-amber-700 dark:text-amber-300">
                  {{ t("admin.settings.telegram.configuration.tokenKeepWarning") }}
                </p>
              </div>

              <div class="space-y-2">
                <Input
                  id="telegram-webhook-url"
                  v-model="webhookUrl"
                  :label="t('admin.settings.telegram.configuration.webhookUrl')"
                  :placeholder="t('admin.settings.telegram.configuration.webhookPlaceholder')"
                  :hint="t('admin.settings.telegram.configuration.webhookHelp')"
                />
                <div class="flex sm:justify-end">
                  <button
                    type="button"
                    class="btn btn-secondary btn-sm w-full sm:w-auto"
                    @click="fillCurrentWebhookUrl"
                  >
                    <Icon name="globe" size="sm" />
                    {{ t("admin.settings.telegram.actions.fillCurrentDomain") }}
                  </button>
                </div>
              </div>
            </div>

            <div class="flex flex-col gap-3 border-t border-gray-100 pt-4 text-sm sm:flex-row sm:items-center sm:justify-between dark:border-dark-700">
              <div class="flex flex-wrap gap-x-5 gap-y-1 text-gray-500 dark:text-gray-400">
                <span>{{ t("admin.settings.telegram.configuration.source") }}: {{ configSourceText }}</span>
                <span>{{ t("admin.settings.telegram.configuration.lifecycle") }}: {{ lifecycleStatusText }}</span>
              </div>
              <button
                type="button"
                class="btn btn-primary btn-sm shrink-0"
                :disabled="saving"
                @click="requestSave"
              >
                <Icon :name="saving ? 'refresh' : 'checkCircle'" size="sm" :class="saving ? 'animate-spin' : ''" />
                {{ saving ? t("admin.settings.telegram.actions.saving") : t("admin.settings.telegram.actions.save") }}
              </button>
            </div>
          </div>

          <div
            class="flex flex-col gap-3 rounded-lg border p-4 sm:flex-row sm:items-center sm:justify-between"
            :class="statusBlockClass"
          >
            <div class="flex items-start gap-3">
              <Icon :name="statusIcon" size="md" class="mt-0.5 shrink-0" />
              <div>
                <p class="text-sm font-medium">{{ statusText }}</p>
                <p class="mt-1 text-sm opacity-80">{{ statusHint }}</p>
              </div>
            </div>
            <a
              v-if="botLink"
              :href="botLink"
              target="_blank"
              rel="noopener noreferrer"
              class="btn btn-secondary btn-sm shrink-0"
            >
              <Icon name="externalLink" size="sm" />
              {{ t("admin.settings.telegram.openBot") }}
            </a>
          </div>

          <div v-if="botUsername" class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <Icon name="chat" size="sm" class="text-gray-400" />
            <span>{{ t("admin.settings.telegram.botUsername") }}</span>
            <code class="select-all font-mono text-gray-900 dark:text-white">@{{ botUsername }}</code>
          </div>

          <div v-if="isReady" class="border-t border-gray-100 pt-5 dark:border-dark-700">
            <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
              <div>
                <h3 class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t("admin.settings.telegram.verification.title") }}
                </h3>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                  {{ t("admin.settings.telegram.verification.instruction") }}
                </p>
              </div>
              <button
                type="button"
                class="btn btn-primary btn-sm shrink-0"
                :disabled="generating"
                @click="generateCode"
              >
                <Icon :name="code ? 'refresh' : 'plus'" size="sm" />
                {{ generating ? t("admin.settings.telegram.actions.generating") : code || hasPendingCode ? t("admin.settings.telegram.actions.regenerate") : t("admin.settings.telegram.actions.generate") }}
              </button>
            </div>

            <div v-if="code" class="mt-5 flex flex-col gap-3 rounded-lg border border-primary-200 bg-primary-50 p-4 dark:border-primary-900/60 dark:bg-primary-950/20 sm:flex-row sm:items-center sm:justify-between">
              <div>
                <p class="text-xs font-medium uppercase text-primary-700 dark:text-primary-300">
                  {{ t("admin.settings.telegram.verification.code") }}
                </p>
                <code class="mt-1 block select-all font-mono text-2xl font-semibold text-gray-900 dark:text-white">
                  {{ formattedCode }}
                </code>
              </div>
              <div class="flex items-center gap-3">
                <button
                  type="button"
                  class="btn btn-secondary btn-sm btn-icon"
                  :aria-label="t('admin.settings.telegram.actions.copyCode')"
                  :title="t('admin.settings.telegram.actions.copyCode')"
                  @click="copyCode"
                >
                  <Icon name="copy" size="sm" />
                </button>
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="cancelling"
                  @click="cancelCode"
                >
                  <Icon name="x" size="sm" />
                  {{ cancelling ? t("admin.settings.telegram.actions.cancelling") : t("admin.settings.telegram.actions.cancel") }}
                </button>
              </div>
            </div>

            <p v-else-if="hasPendingCode" class="mt-5 text-sm text-amber-700 dark:text-amber-300">
              {{ t("admin.settings.telegram.verification.pending") }}
            </p>
            <p v-else-if="codeExpired" class="mt-5 text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.telegram.verification.expired") }}
            </p>
          </div>
        </template>

        <p v-else class="text-sm text-red-600 dark:text-red-400">
          {{ t("admin.settings.telegram.errors.load") }}
        </p>
      </div>
    </section>

    <section v-if="status" class="card">
      <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
          {{ t("admin.settings.telegram.bindings.title") }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.settings.telegram.bindings.description") }}
        </p>
      </div>

      <div v-if="status.bindings.length === 0" class="p-6 text-sm text-gray-500 dark:text-gray-400">
        {{ t("admin.settings.telegram.bindings.empty") }}
      </div>

      <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
        <li v-for="binding in status.bindings" :key="binding.id" class="flex flex-col gap-4 px-6 py-4 sm:flex-row sm:items-center sm:justify-between">
          <div class="min-w-0 space-y-1 text-sm">
            <p class="font-medium text-gray-900 dark:text-white">{{ bindingDisplayName(binding) }}</p>
            <p class="break-all text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.telegram.bindings.telegramId", { id: binding.telegram_user_id }) }}
            </p>
            <p class="text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.telegram.bindings.boundAt", { time: formatDateTime(binding.bound_at) }) }}
            </p>
          </div>
          <button
            type="button"
            class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
            :disabled="revokingId === binding.id"
            @click="bindingToRevoke = binding"
          >
            <Icon name="trash" size="sm" />
            {{ revokingId === binding.id ? t("admin.settings.telegram.actions.revoking") : t("admin.settings.telegram.actions.revoke") }}
          </button>
        </li>
      </ul>
    </section>

    <ConfirmDialog
      :show="Boolean(bindingToRevoke)"
      :title="t('admin.settings.telegram.confirm.title')"
      :message="t('admin.settings.telegram.confirm.message')"
      :confirm-text="t('admin.settings.telegram.actions.revoke')"
      danger
      @cancel="bindingToRevoke = null"
      @confirm="revokeBinding"
    />
    <ConfirmDialog
      :show="confirmingDisable"
      :title="t('admin.settings.telegram.configuration.disableConfirmTitle')"
      :message="t('admin.settings.telegram.configuration.disableConfirmMessage')"
      :confirm-text="t('admin.settings.telegram.configuration.disable')"
      danger
      @cancel="confirmingDisable = false"
      @confirm="saveConfig"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api";
import type {
  TelegramBinding,
  TelegramBotConfig,
  TelegramStatus,
  UpdateTelegramBotConfigRequest,
} from "@/api/admin/settings";
import ConfirmDialog from "@/components/common/ConfirmDialog.vue";
import Input from "@/components/common/Input.vue";
import Toggle from "@/components/common/Toggle.vue";
import Icon from "@/components/icons/Icon.vue";
import { useClipboard } from "@/composables/useClipboard";
import { useAppStore } from "@/stores";
import { extractI18nErrorMessage } from "@/utils/apiError";
import { formatDateTime } from "@/utils/format";

const { t } = useI18n();
const appStore = useAppStore();
const { copyToClipboard } = useClipboard();

const config = ref<TelegramBotConfig | null>(null);
const status = ref<TelegramStatus | null>(null);
const loading = ref(true);
const saving = ref(false);
const generating = ref(false);
const cancelling = ref(false);
const revokingId = ref<number | null>(null);
const bindingToRevoke = ref<TelegramBinding | null>(null);
const confirmingDisable = ref(false);
const enabled = ref(false);
const botToken = ref("");
const webhookUrl = ref("");
const code = ref<string | null>(null);
const codeExpired = ref(false);
const telegramWebhookPath = "/api/v1/telegram/webhook";
let expiryTimer: number | null = null;
let statusPollTimer: number | null = null;

const isReady = computed(() =>
  Boolean(
    effectiveEnabled.value &&
      tokenConfigured.value &&
      webhookConfigured.value &&
      config.value?.lifecycle_status === "ready",
  ),
);
const effectiveEnabled = computed(() => config.value?.enabled ?? status.value?.enabled ?? false);
const tokenConfigured = computed(() => config.value?.token_configured ?? status.value?.configured ?? false);
const webhookConfigured = computed(() =>
  config.value?.webhook_configured ?? status.value?.webhook_configured ?? false,
);
const botUsername = computed(() => config.value?.bot_username || status.value?.bot_username || "");
const botLink = computed(() => {
  const username = botUsername.value;
  return username ? `https://t.me/${username}` : null;
});
const hasPendingCode = computed(
  () => !code.value && Boolean(status.value?.pending_expires_at && new Date(status.value.pending_expires_at).getTime() > Date.now()),
);
const formattedCode = computed(() =>
	code.value ? (code.value.match(/.{1,5}/g)?.join(" ") ?? code.value) : "",
);
const statusText = computed(() => {
  if (!effectiveEnabled.value) return t("admin.settings.telegram.status.disabled");
  if (!tokenConfigured.value) return t("admin.settings.telegram.status.tokenMissing");
  if (!webhookConfigured.value) return t("admin.settings.telegram.status.webhookMissing");
  return t("admin.settings.telegram.status.ready");
});
const statusHint = computed(() => {
  if (!effectiveEnabled.value) return t("admin.settings.telegram.status.disabledHint");
  if (!tokenConfigured.value) return t("admin.settings.telegram.status.tokenMissingHint");
  if (!webhookConfigured.value) return t("admin.settings.telegram.status.webhookMissingHint");
  return t("admin.settings.telegram.status.readyHint");
});
const statusIcon = computed(() => (isReady.value ? "checkCircle" : "exclamationCircle"));
const statusBlockClass = computed(() =>
  isReady.value
    ? "border-green-200 bg-green-50 text-green-800 dark:border-green-900/60 dark:bg-green-950/20 dark:text-green-200"
    : "border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/20 dark:text-amber-200",
);
const tokenStatusText = computed(() =>
  tokenConfigured.value
    ? t("admin.settings.telegram.configuration.tokenConfigured")
    : t("admin.settings.telegram.configuration.tokenNotConfigured"),
);
const configSourceText = computed(() => {
  const source = config.value?.config_source;
  if (!source) return t("admin.settings.telegram.configuration.sourceUnknown");
  const key = `admin.settings.telegram.configuration.sources.${source}`;
  const translated = t(key);
  return translated === key
    ? t("admin.settings.telegram.configuration.sourceUnknown")
    : translated;
});
const lifecycleStatusText = computed(() => {
  const lifecycle = config.value?.lifecycle_status;
  if (!lifecycle) return t("admin.settings.telegram.configuration.lifecycleUnknown");
  const key = `admin.settings.telegram.configuration.lifecycleStates.${lifecycle}`;
  const translated = t(key);
  return translated === key
    ? t("admin.settings.telegram.configuration.lifecycleUnknown")
    : translated;
});

function stopCodeTracking(): void {
  if (expiryTimer !== null) {
    window.clearTimeout(expiryTimer);
    expiryTimer = null;
  }
  if (statusPollTimer !== null) {
    window.clearInterval(statusPollTimer);
    statusPollTimer = null;
  }
}

function clearDisplayedCode(expired = true): void {
  stopCodeTracking();
  code.value = null;
  codeExpired.value = expired;
}

async function pollActiveCodeStatus(): Promise<void> {
  try {
    const nextStatus = await adminAPI.settings.getTelegramStatus();
    status.value = nextStatus;
    if (!nextStatus.pending_expires_at) {
      clearDisplayedCode();
    }
  } catch {
    // A transient polling error should not interrupt the active code workflow.
  }
}

function startCodeTracking(nextCode: string, nextExpiresAt: string): void {
  stopCodeTracking();
  code.value = nextCode;
  codeExpired.value = false;

  const timeoutMs = new Date(nextExpiresAt).getTime() - Date.now();
  if (timeoutMs <= 0) {
    clearDisplayedCode();
    void pollActiveCodeStatus();
    return;
  }

  expiryTimer = window.setTimeout(() => {
    clearDisplayedCode();
    void pollActiveCodeStatus();
  }, timeoutMs);
  statusPollTimer = window.setInterval(() => void pollActiveCodeStatus(), 4000);
}

function applyConfig(nextConfig: TelegramBotConfig): void {
  config.value = nextConfig;
  enabled.value = nextConfig.enabled;
  if (nextConfig.webhook_url) {
    webhookUrl.value = nextConfig.webhook_url;
  } else if (!webhookUrl.value) {
    fillCurrentWebhookUrl();
  }
}

function fillCurrentWebhookUrl(): void {
  webhookUrl.value = new URL(telegramWebhookPath, window.location.origin).toString();
}

async function loadConfig(): Promise<void> {
  try {
    applyConfig(await adminAPI.settings.getTelegramConfig());
  } catch (error: unknown) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.loadConfig"),
      ),
    );
  }
}

async function loadStatus(): Promise<void> {
  try {
    status.value = await adminAPI.settings.getTelegramStatus();
  } catch (error: unknown) {
    status.value = null;
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.load"),
      ),
    );
  }
}

async function loadInitialState(): Promise<void> {
  loading.value = true;
  await Promise.all([loadConfig(), loadStatus()]);
  loading.value = false;
}

function requestSave(): void {
  const nextToken = botToken.value.trim();
  const nextWebhookUrl = webhookUrl.value.trim();

  if (enabled.value && !tokenConfigured.value && !nextToken) {
    appStore.showError(t("admin.settings.telegram.errors.tokenRequired"));
    return;
  }
  if (enabled.value && !nextWebhookUrl) {
    appStore.showError(t("admin.settings.telegram.errors.webhookRequired"));
    return;
  }
  if (effectiveEnabled.value && !enabled.value) {
    confirmingDisable.value = true;
    return;
  }
  void saveConfig();
}

async function saveConfig(): Promise<void> {
  confirmingDisable.value = false;
  saving.value = true;
  const nextToken = botToken.value.trim();
  const nextWebhookUrl = webhookUrl.value.trim();
  const payload: UpdateTelegramBotConfigRequest = {
    enabled: enabled.value,
    webhook_url: nextWebhookUrl,
  };
  if (nextToken) {
    payload.bot_token = nextToken;
  }

  try {
    applyConfig(await adminAPI.settings.updateTelegramConfig(payload));
    botToken.value = "";
    if (!enabled.value) {
      clearDisplayedCode(false);
    }
    await loadStatus();
    appStore.showSuccess(t("admin.settings.telegram.success.configurationSaved"));
  } catch (error: unknown) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.save"),
      ),
    );
  } finally {
    saving.value = false;
  }
}

async function generateCode(): Promise<void> {
  generating.value = true;
  try {
    const result = await adminAPI.settings.generateTelegramVerificationCode();
    if (status.value) {
      status.value = { ...status.value, pending_expires_at: result.expires_at };
    }
    startCodeTracking(result.code, result.expires_at);
    appStore.showSuccess(t("admin.settings.telegram.success.codeGenerated"));
  } catch (error: unknown) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.generate"),
      ),
    );
  } finally {
    generating.value = false;
  }
}

async function cancelCode(): Promise<void> {
  cancelling.value = true;
  try {
    await adminAPI.settings.cancelTelegramVerificationCode();
    clearDisplayedCode(false);
    if (status.value) {
      status.value = { ...status.value, pending_expires_at: null };
    }
    appStore.showSuccess(t("admin.settings.telegram.success.codeCancelled"));
  } catch (error: unknown) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.cancel"),
      ),
    );
  } finally {
    cancelling.value = false;
  }
}

function copyCode(): void {
  if (code.value) {
    void copyToClipboard(code.value, t("admin.settings.telegram.success.codeCopied"));
  }
}

function bindingDisplayName(binding: TelegramBinding): string {
  if (binding.display_name) return binding.display_name;
  if (binding.username) return `@${binding.username}`;
  return t("admin.settings.telegram.bindings.unnamed");
}

async function revokeBinding(): Promise<void> {
  const binding = bindingToRevoke.value;
  if (!binding) return;

  revokingId.value = binding.id;
  try {
    await adminAPI.settings.revokeTelegramBinding(binding.id);
    bindingToRevoke.value = null;
    await loadStatus();
    appStore.showSuccess(t("admin.settings.telegram.success.bindingRevoked"));
  } catch (error: unknown) {
    appStore.showError(
      extractI18nErrorMessage(
        error,
        t,
        "admin.settings.telegram.errors",
        t("admin.settings.telegram.errors.revoke"),
      ),
    );
  } finally {
    revokingId.value = null;
  }
}

onMounted(() => {
  void loadInitialState();
});

onUnmounted(stopCodeTracking);
</script>
