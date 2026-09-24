/** Pending login started by StartLogin; the code arrives by email. */
export interface LoginChallenge {
  challengeId: Uint8Array;
  codeExpiresInSeconds: bigint;
}

export type LoginStart = LoginChallenge | { subjectId: Uint8Array };

/** Auth boundary. Identity is established only by a server response. */
export interface SessionApi {
  register(identifier: string, password: Uint8Array): Promise<void>;
  login(identifier: string, password: Uint8Array): Promise<Uint8Array>;
  startLogin(identifier: string, password: Uint8Array): Promise<LoginStart>;
  completeLogin(challengeId: Uint8Array, code: string): Promise<Uint8Array>;
  resendLoginCode(challengeId: Uint8Array): Promise<LoginChallenge>;
  requestEmailVerification(email: string): Promise<void>;
  confirmEmail(token: string): Promise<Uint8Array | null>;
  refresh(): Promise<void>;
  logout(): Promise<void>;
  logoutAll(): Promise<void>;
  getIdentity(): Promise<{ subjectId: Uint8Array } | null>;
}
