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

export type CalendarEntry = components['schemas']['CalendarEntryDTO'];
export type CalendarWindow = components['schemas']['CalendarWindowDTO'];
