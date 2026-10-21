// How the Worker tells the owner something needs them (plan D8): email through the send_email binding to
// the owner's verified address, or nothing when that is not configured. The triggers decide when.
import type { HandlerContext } from "../routes.ts";

export interface Notification {
  subject: string;
  text: string;
}

export interface Notifier {
  /** Resolves once the message is handed over; rejects when it was not. */
  send(notification: Notification): Promise<void>;
}

/** Sends through the binding (developers.cloudflare.com email-routing/email-workers/send-email-workers). */
export class EmailNotifier implements Notifier {
  readonly #email: SendEmail;
  readonly #from: string;
  readonly #to: string;

  constructor(email: SendEmail, from: string, to: string) {
    this.#email = email;
    this.#from = from;
    this.#to = to;
  }

  async send({ subject, text }: Notification): Promise<void> {
    await this.#email.send({ to: this.#to, from: this.#from, subject, text });
  }
}

let disabledLogged = false;

/**
 * Stands in when email is not configured. The triggers still claim each notification through it, so
 * turning email on later does not replay every question and state change seen while it was off.
 */
export class DisabledNotifier implements Notifier {
  async send(): Promise<void> {
    if (disabledLogged) return;
    disabledLogged = true;
    console.log(JSON.stringify({ notify: "disabled" }));
  }
}

export function notifierFor({ env, cfg }: HandlerContext): Notifier {
  if (env.EMAIL && cfg.notifyFrom && cfg.notifyTo) return new EmailNotifier(env.EMAIL, cfg.notifyFrom, cfg.notifyTo);
  return new DisabledNotifier();
}
