import type { Messages } from '../types.ts';
import { deCommon } from './common.ts';
import { deLanguage } from './language.ts';
import { deValidation } from './validation.ts';

/**
 * The German catalogue, typed as Messages: a key missing here, an extra
 * one, or a string where English has a message function fails `tsc -b`.
 * catalogue.test.ts additionally refuses an empty German string.
 *
 * German style (spec "German style") — fixed once, here:
 *
 *   Run                → Lauf
 *   Template           → Vorlage
 *   Inventory          → Inventar
 *   Credential         → Zugangsdaten
 *   Jump host          → Sprunghost
 *   Schedule           → Zeitplan
 *   Channel            → Kanal
 *   Alert              → Alarm
 *   Maintenance window → Wartungsfenster
 *   Settings           → Einstellungen
 *
 *   Unchanged: Playbook, Host, Worker, Agent, Check, Token, Zone.
 *
 * Neutral imperatives and nouns, no "du" and, where avoidable, no "Sie":
 * "Speichern", "Host hinzufügen", "Passwort ändern". Validation messages
 * stay lower case, like their English originals.
 */
export const de: Messages = {
  common: deCommon,
  language: deLanguage,
  validation: deValidation,
};
