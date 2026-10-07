/**
 * Convenience aliases onto the generated schema's component types
 * (src/api/schema.d.ts, produced by `npm run api:gen` from the manager's
 * own OpenAPI document — see that script and internal/api/openapi_doc.go).
 * Pages and hooks import from here instead of reaching into
 * `components['schemas'][...]` themselves, so a schema rename only needs
 * fixing in one place.
 */
import type { components } from './schema.d.ts';

export type Host = components['schemas']['HostDTO'];
export type Zone = components['schemas']['ZoneDTO'];
export type Tag = components['schemas']['TagDTO'];
export type Member = components['schemas']['MemberDTO'];
export type Event = components['schemas']['EventDTO'];
export type Token = components['schemas']['TokenDTO'];
export type Passkey = components['schemas']['PasskeyDTO'];
export type Org = components['schemas']['OrgDTO'];
export type User = components['schemas']['UserStruct'];

export type HostInput = components['schemas']['HostInputBody'];
export type SetupCompleteInput = components['schemas']['SetupCompleteInputBody'];

export type InventoryGroupSummary = components['schemas']['GroupSummaryDTO'];
export type InventoryGroup = components['schemas']['GroupDTO'];
export type InventoryMember = components['schemas']['InventoryMemberDTO'];
export type Credential = components['schemas']['CredentialDTO'];
export type Bastion = components['schemas']['BastionDTO'];
export type HostSSH = components['schemas']['HostSSHDTO'];

export type Repo = components['schemas']['RepoDTO'];
export type Playbook = components['schemas']['PlaybookDTO'];
export type Template = components['schemas']['TemplateDTO'];
export type Job = components['schemas']['JobDTO'];
export type JobDetail = components['schemas']['JobDetailDTO'];
export type JobEvent = components['schemas']['JobEventDTO'];
export type JobTemplateSummary = components['schemas']['JobTemplateSummaryDTO'];
export type HostTest = components['schemas']['HostTestDTO'];

export type RepoInput = components['schemas']['RepoCreateInputBody'];
export type TemplateInput = components['schemas']['TemplateCreateInputBody'];
export type BootstrapInput = components['schemas']['BootstrapInputBody'];
export type AdhocInput = components['schemas']['AdhocInputBody'];

export type Schedule = components['schemas']['ScheduleDTO'];
export type ScheduleRun = components['schemas']['ScheduleRunDTO'];
export type ScheduleInput = components['schemas']['ScheduleBody'];

export type Trigger = components['schemas']['TriggerDTO'];
export type TriggerInput = components['schemas']['TriggerBody'];
export type TriggerDelivery = components['schemas']['TriggerDeliveryDTO'];

export type Agent = components['schemas']['AgentDTO'];
export type AgentToken = components['schemas']['AgentTokenDTO'];

export type Worker = components['schemas']['WorkerDTO'];
export type WorkerEnrollment = components['schemas']['WorkerEnrollmentDTO'];

export type NotifyChannel = components['schemas']['NotifyChannelDTO'];
export type NotifyChannelInput = components['schemas']['NotifyChannelBody'];
export type NotifyDelivery = components['schemas']['NotifyDeliveryDTO'];

export type MaintenanceWindow = components['schemas']['MaintenanceWindowDTO'];
export type MaintenanceWindowInput = components['schemas']['MaintenanceWindowBody'];

export type Rule = components['schemas']['RuleDTO'];
export type RuleInput = components['schemas']['RuleBody'];
// Named RuleFormValue, not RuleForm: features/checks/RuleForm.tsx already
// owns that name for the form-mode editor component.
export type RuleFormValue = components['schemas']['RuleFormDTO'];
export type RuleChecked = components['schemas']['RuleCheckedDTO'];
export type RuleMetric = components['schemas']['RuleMetricDTO'];
export type RuleScope = Rule['scope'];
export type RuleMode = Rule['mode'];

export type ItemDecision = components['schemas']['ItemDecisionDTO'];
export type ItemDecisionInput = components['schemas']['ItemDecisionBody'];
export type ItemDecisionScope = ItemDecision['scope'];
export type ItemDecisionMatchKind = ItemDecision['match_kind'];
export type ItemDecisionVerdict = ItemDecision['decision'];
// POST /item-decisions/preview takes the same ItemDecisionBody shape but
// reads only the fields that decide what a pattern matches — see
// items.Service.Preview's own doc comment (internal/items/service.go) for
// why decision and note play no part in it.
export type ItemDecisionPreviewInput = Omit<ItemDecisionInput, 'decision' | 'note'>;

// Item is GET /hosts/{id}/items' and GET /items' own row: a discovered
// (check_key, item) pair, its effective verdict, and — when one matched —
// the decision that produced it. `decided_by` is set for both an ignore
// AND an overruling watch (review focus 3: "an organization pattern and a
// host watch on the same item"), never only for the ignored case.
export type Item = components['schemas']['ItemDTO'];

export type CalendarEntry = components['schemas']['CalendarEntryDTO'];
export type CalendarWindow = components['schemas']['CalendarWindowDTO'];

export type CheckDefinition = components['schemas']['CheckDefinitionDTO'];
export type CheckDefinitionInput = components['schemas']['CheckDefinitionBody'];
export type CheckPlugin = CheckDefinition['plugin'];
export type CheckBindingKind = CheckDefinition['binding_kind'];
export type MonitoringGroup = components['schemas']['MonitoringGroupDTO'];
export type MonitoringGroupInput = components['schemas']['MonitoringGroupBody'];
export type MonitoringGroupPreviewHost = components['schemas']['PreviewHostDTO'];
export type HostChecks = components['schemas']['HostChecksOutputBody'];
export type HostCheck = components['schemas']['HostCheckDTO'];
export type HostCheckValue = components['schemas']['HostCheckValueDTO'];
export type MetricPoint = components['schemas']['MetricPointDTO'];

export type EffectiveRule = components['schemas']['EffectiveRuleDTO'];

export type FleetHost = components['schemas']['FleetHostDTO'];
export type FleetTotals = components['schemas']['FleetTotalsDTO'];
export type FleetState = NonNullable<FleetHost['state']>;

// Alert is one episode (v0.4a design, "one episode, one alert"): List's own
// row, already flattened with its Suppression verdict (muted/mute_reason/
// muted_until/mute_window_id) by the server — see alert_handlers.go's own
// toAlertDTO. `severity` is the alert's own tracked current severity, which
// the lifecycle never sets back to "ok" even while the underlying tuple
// reads ok during a flap (decision 6) — it is not the same thing as "what
// the tuple reads right now", which this DTO does not carry at all; v0.4a's
// list only shows severity/peak_severity/open-ness, exactly the three facts
// the design doc's GUI section calls for.
export type Alert = components['schemas']['AlertDTO'];
export type AlertScope = Alert['scope'];
export type AlertSeverity = NonNullable<Alert['severity']>;
export type AlertCloseReason = NonNullable<Alert['closed_reason']>;
export type AlertMuteReason = NonNullable<Alert['mute_reason']>;
export type AlertAcknowledgeInput = components['schemas']['AlertAcknowledgeInputBody'];
export type AlertSnoozeInput = components['schemas']['AlertSnoozeInputBody'];

/**
 * The windows GET /hosts/{id}/metrics accepts, and nothing else: the route
 * refuses any other value with a 400 naming these three
 * (internal/api/host_check_handlers.go). The generated schema types
 * `range` as a bare string — huma validates it by hand there so the
 * refusal carries a readable message — so the closed set is spelled out
 * here rather than left to a free-text query parameter the GUI could
 * silently get wrong.
 */
export type MetricRange = '1h' | '24h' | '7d';
