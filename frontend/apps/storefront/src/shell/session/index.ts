import { shallowRef, readonly, type Ref } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import type {
  PublicApi,
  Profile,
  ProfileInput,
  AddressBook,
  AddressWrite,
  AddressSelection,
  AccountSettings,
  SettingsInput,
} from '../../shared/api/types';
import { isProfilePending } from '../../shared/api/errors';

export type SessionStatus =
  | 'unknown'
  | 'checking'
  | 'authenticated'
  | 'profilePending'
  | 'anonymous'
  | 'unavailable'
  | 'uncertain'
  | 'unsupported'
  | 'switching'
  | 'signingOut';
export interface SessionState {
  status: SessionStatus;
  generation: string;
  subjectId: string | null;
}
export interface SessionGuard {
  generation: string;
  subjectId: string | null;
}
export type SessionKind = 'bootstrap' | 'login' | 'refresh' | 'logout' | 'logoutAll' | 'expired';
export interface SessionJournal {
  generation: string;
  operationId: string;
  kind: SessionKind;
  phase: 'pending' | 'settled' | 'uncertain';
}
export interface SessionNotice {
  generation: string;
  operationId?: string;
  kind: SessionKind;
  phase: SessionJournal['phase'] | 'intent';
  previousGeneration?: string;
}
/** Implementations coordinate the same lock and journal across all same-origin tabs. */
export interface SessionEnvironment {
  lock<T>(mode: 'shared' | 'exclusive', work: () => Promise<T>): Promise<T>;
  read(): SessionJournal | null;
  write(journal: SessionJournal): void;
  publish(notice: SessionNotice): void;
  subscribe(listener: (notice?: SessionNotice) => void): () => void;
  randomId(): string;
  dispose?(): void;
}
export interface SessionController {
  readonly state: Readonly<Ref<SessionState>>;
  bootstrap(): Promise<void>;
  register(identifier: string, password: Uint8Array): Promise<void>;
  login(identifier: string, password: Uint8Array): Promise<void>;
  logout(all?: boolean): Promise<void>;
  capture(): SessionGuard;
  readProfile(guard?: SessionGuard): Promise<Profile>;
  updateProfile(input: ProfileInput, guard: SessionGuard): Promise<Profile>;
  readSettings(guard: SessionGuard): Promise<AccountSettings>;
  updateSettings(input: SettingsInput, guard: SessionGuard): Promise<AccountSettings>;
  readAddresses(guard: SessionGuard): Promise<AddressBook>;
  createAddress(input: AddressWrite, guard: SessionGuard): Promise<AddressBook>;
  updateAddress(input: AddressWrite & AddressSelection, guard: SessionGuard): Promise<AddressBook>;
  deleteAddress(input: AddressSelection, guard: SessionGuard): Promise<AddressBook>;
  setDefaultAddress(input: AddressSelection, guard: SessionGuard): Promise<AddressBook>;
  dispose(): void;
}
export class GuardMismatchError extends Error {
  constructor() {
    super('Session changed; reload the current account before saving.');
    this.name = 'GuardMismatchError';
  }
}
export class SessionUnavailableError extends Error {
  constructor() {
    super('Safe browser session coordination is unavailable.');
    this.name = 'SessionUnavailableError';
  }
}
export class SessionUncertainError extends Error {
  constructor() {
    super('The session operation outcome is unknown. Sign in again.');
    this.name = 'SessionUncertainError';
  }
}

const LOCK = 'marketmesh:browser-session:v1';
const JOURNAL = 'marketmesh:session-operation:v1';
const kinds: SessionKind[] = ['bootstrap', 'login', 'refresh', 'logout', 'logoutAll', 'expired'];
function parseJournal(raw: string | null): SessionJournal | null {
  if (raw === null) return null;
  const value: unknown = JSON.parse(raw);
  if (!value || typeof value !== 'object') throw new SessionUnavailableError();
  const item = value as Record<string, unknown>;
  if (
    Object.keys(item).length !== 4 ||
    typeof item.generation !== 'string' ||
    !item.generation ||
    item.generation.length > 128 ||
    typeof item.operationId !== 'string' ||
    !item.operationId ||
    item.operationId.length > 128 ||
    !kinds.includes(item.kind as SessionKind) ||
    !['pending', 'settled', 'uncertain'].includes(item.phase as string)
  )
    throw new SessionUnavailableError();
  return item as unknown as SessionJournal;
}
/** No tokens, subjects, profile data, or credentials are stored or broadcast. */
export function browserSessionEnvironment(): SessionEnvironment | null {
  if (typeof window === 'undefined' || !navigator.locks || !crypto.randomUUID) return null;
  try {
    const probe = `${JOURNAL}:probe:${crypto.randomUUID()}`;
    localStorage.setItem(probe, '1');
    localStorage.removeItem(probe);
    parseJournal(localStorage.getItem(JOURNAL));
    const channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(LOCK);
    return {
      lock: (mode, work) => navigator.locks.request(LOCK, { mode }, work),
      read: () => parseJournal(localStorage.getItem(JOURNAL)),
      write: (journal) => localStorage.setItem(JOURNAL, JSON.stringify(journal)),
      publish: (notice) => channel?.postMessage(notice),
      subscribe(listener) {
        const message = (event: MessageEvent<SessionNotice>) => listener(event.data);
        const storage = (event: StorageEvent) => {
          if (event.key === JOURNAL || event.key === null) listener();
        };
        channel?.addEventListener('message', message);
        window.addEventListener('storage', storage);
        return () => {
          channel?.removeEventListener('message', message);
          window.removeEventListener('storage', storage);
        };
      },
      randomId: () => crypto.randomUUID(),
      dispose: () => channel?.close(),
    };
  } catch {
    return null;
  }
}
const subject = (id: Uint8Array): string =>
  Array.from(id, (value) => value.toString(16).padStart(2, '0')).join('');
const unauthenticated = (error: unknown): boolean =>
  error instanceof ConnectError && error.code === Code.Unauthenticated;
const definitive = (error: unknown): boolean =>
  error instanceof ConnectError &&
  [Code.Unauthenticated, Code.InvalidArgument, Code.PermissionDenied].includes(error.code);

export function createSessionController(
  api: PublicApi,
  options: { environment?: SessionEnvironment | null } = {},
): SessionController {
  const env = options.environment === undefined ? browserSessionEnvironment() : options.environment;
  const current = shallowRef<SessionState>({
    status: env ? 'unknown' : 'unsupported',
    generation: '',
    subjectId: null,
  });
  let disposed = false;
  let revision = 0;
  let bootstrapFlight: Promise<void> | null = null;
  let ownOperation: string | null = null;
  let knownIdentity: SessionGuard | null = null;
  let mismatchedGeneration: string | null = null;
  function available(): SessionEnvironment {
    if (!env || disposed || current.value.status === 'unsupported')
      throw new SessionUnavailableError();
    return env;
  }
  function set(
    status: SessionStatus,
    generation = current.value.generation,
    subjectId: string | null = current.value.subjectId,
  ) {
    if (!disposed) current.value = { status, generation, subjectId };
  }
  function readJournal(): SessionJournal | null {
    try {
      return available().read();
    } catch {
      set('unsupported', current.value.generation, null);
      throw new SessionUnavailableError();
    }
  }
  function write(journal: SessionJournal) {
    try {
      available().write(journal);
    } catch {
      set('unsupported', journal.generation, null);
      throw new SessionUnavailableError();
    }
    // Broadcast is advisory; the durable journal remains authoritative.
    try {
      env?.publish({
        generation: journal.generation,
        kind: journal.kind,
        phase: journal.phase,
        operationId: journal.operationId,
      });
    } catch {
      /* storage event still invalidates other tabs */
    }
  }
  async function initialize() {
    const environment = available();
    await environment.lock('exclusive', async () => {
      let journal = readJournal();
      if (!journal) {
        journal = {
          generation: environment.randomId(),
          operationId: environment.randomId(),
          kind: 'bootstrap',
          phase: 'settled',
        };
        write(journal);
      }
    });
  }
  function assertGuard(guard: SessionGuard, journal: SessionJournal | null) {
    if (journal && journal.phase !== 'settled') {
      revision++;
      set('uncertain', journal.generation, null);
    }
    if (
      !journal ||
      journal.phase !== 'settled' ||
      journal.generation !== guard.generation ||
      current.value.generation !== guard.generation ||
      current.value.subjectId !== guard.subjectId ||
      disposed
    )
      throw new GuardMismatchError();
  }
  function rejectIdentity(generation: string): never {
    mismatchedGeneration = generation;
    revision++;
    set('uncertain', generation, null);
    throw new GuardMismatchError();
  }
  function rememberIdentity(generation: string, id: string) {
    if (
      mismatchedGeneration === generation ||
      (knownIdentity?.generation === generation && knownIdentity.subjectId !== id)
    )
      rejectIdentity(generation);
    knownIdentity = { generation, subjectId: id };
  }
  async function probe(
    generation: string,
    apply = true,
    propagatePending = false,
  ): Promise<Profile | undefined> {
    if (current.value.generation !== generation) throw new GuardMismatchError();
    const attempt = revision;
    try {
      const profile = await api.getProfile();
      if (
        disposed ||
        attempt !== revision ||
        current.value.generation !== generation ||
        readJournal()?.generation !== generation
      )
        throw new GuardMismatchError();
      rememberIdentity(generation, subject(profile.subjectId));
      if (apply) set('authenticated', generation, subject(profile.subjectId));
      return profile;
    } catch (error) {
      if (disposed || attempt !== revision || error instanceof GuardMismatchError)
        throw new GuardMismatchError();
      if (isProfilePending(error)) {
        if (apply) set('profilePending', generation);
        if (propagatePending) throw error;
        return;
      }
      throw error;
    }
  }
  function begin(kind: SessionKind): SessionJournal {
    const environment = available();
    const refreshing = kind === 'refresh';
    const journal: SessionJournal = {
      generation: refreshing
        ? (readJournal()?.generation ?? environment.randomId())
        : environment.randomId(),
      operationId: environment.randomId(),
      kind,
      phase: 'pending',
    };
    if (!refreshing) revision++;
    ownOperation = journal.operationId;
    set(
      kind === 'logout' || kind === 'logoutAll'
        ? 'signingOut'
        : kind === 'login'
          ? 'switching'
          : 'checking',
      journal.generation,
      refreshing ? current.value.subjectId : null,
    );
    write(journal);
    return journal;
  }
  function finish(journal: SessionJournal, phase: SessionJournal['phase']) {
    write({ ...journal, phase });
    ownOperation = null;
  }
  async function refreshUnderLock(journal: SessionJournal, apply = true): Promise<boolean> {
    if (
      journal.phase !== 'settled' ||
      journal.kind === 'logout' ||
      journal.kind === 'logoutAll' ||
      journal.kind === 'expired'
    ) {
      set(journal.phase === 'settled' ? 'anonymous' : 'uncertain', journal.generation, null);
      return false;
    }
    // Another tab may have already rotated. Never refresh before rechecking.
    try {
      await probe(journal.generation, apply);
      return true;
    } catch (error) {
      if (!unauthenticated(error)) throw error;
    }
    const operation = begin('refresh');
    try {
      await api.refresh();
    } catch (error) {
      if (definitive(error)) {
        const expired = {
          ...operation,
          generation: available().randomId(),
          kind: 'expired' as const,
        };
        finish(expired, 'settled');
        revision++;
        set('anonymous', expired.generation, null);
      } else {
        finish(operation, 'uncertain');
        set('uncertain', operation.generation, null);
      }
      throw error;
    }
    finish(operation, 'settled');
    await probe(operation.generation, apply);
    return true;
  }
  // Only classify a journal while holding the session lock. A pending writer
  // still owning the exclusive lock is active, not an orphaned operation.
  function prepareProbe(journal: SessionJournal | null): journal is SessionJournal {
    if (!journal) throw new SessionUnavailableError();
    if (current.value.generation !== journal.generation) {
      revision++;
      set('unknown', journal.generation, null);
    }
    if (mismatchedGeneration === journal.generation || journal.phase !== 'settled') {
      set('uncertain', journal.generation, null);
      return false;
    }
    if (journal.kind === 'logout' || journal.kind === 'logoutAll' || journal.kind === 'expired') {
      set('anonymous', journal.generation, null);
      return false;
    }
    set('checking', journal.generation);
    return true;
  }
  async function runBootstrap() {
    try {
      await initialize();
      try {
        await available().lock('shared', async () => {
          const journal = readJournal();
          if (!prepareProbe(journal)) return;
          await probe(journal.generation);
        });
      } catch (error) {
        if (!unauthenticated(error)) throw error;
        // Release the shared lock before requesting exclusive ownership. Both
        // phase and identity are reread after all earlier writers have finished.
        await available().lock('exclusive', async () => {
          const latest = readJournal();
          if (!prepareProbe(latest)) return;
          await refreshUnderLock(latest);
        });
      }
    } catch (error) {
      if (error instanceof GuardMismatchError || disposed) return;
      if (current.value.status === 'uncertain' || current.value.status === 'unsupported') return;
      set(unauthenticated(error) ? 'anonymous' : 'unavailable', current.value.generation, null);
    }
  }
  function bootstrap(): Promise<void> {
    if (!bootstrapFlight)
      bootstrapFlight = runBootstrap().finally(() => {
        bootstrapFlight = null;
      });
    return bootstrapFlight;
  }
  function intent(kind: SessionKind) {
    const environment = available();
    const generation = environment.randomId();
    revision++;
    set(kind === 'login' ? 'switching' : 'signingOut', generation, null);
    try {
      environment.publish({
        generation,
        kind,
        phase: 'intent',
        previousGeneration: readJournal()?.generation ?? '',
      });
    } catch {
      /* durable pending follows after lock */
    }
  }
  async function login(identifier: string, password: Uint8Array) {
    intent('login');
    await available().lock('exclusive', async () => {
      const operation = begin('login');
      try {
        const id = await api.login(identifier, password);
        finish(operation, 'settled');
        rememberIdentity(operation.generation, subject(id));
        set('checking', operation.generation, subject(id));
      } catch (error) {
        finish(operation, definitive(error) ? 'settled' : 'uncertain');
        set(definitive(error) ? 'unavailable' : 'uncertain', operation.generation, null);
        throw error;
      }
      try {
        await probe(operation.generation);
      } catch (error) {
        if (!(error instanceof GuardMismatchError)) set('unavailable', operation.generation, null);
        throw error;
      }
    });
  }
  async function logout(all = false) {
    const kind = all ? 'logoutAll' : 'logout';
    intent(kind);
    await available().lock('exclusive', async () => {
      const latest = readJournal();
      if (!latest || latest.phase !== 'settled') {
        set('uncertain', latest?.generation, null);
        throw new SessionUncertainError();
      }
      set('signingOut', latest.generation, null);
      // Logout needs a live access cookie. Restore it at most once before sending.
      try {
        await probe(latest.generation, false);
      } catch (error) {
        if (!unauthenticated(error)) {
          if (!(error instanceof GuardMismatchError)) set('unavailable', latest.generation, null);
          throw error;
        }
        try {
          if (!(await refreshUnderLock(latest, false))) throw new SessionUncertainError();
        } catch (error) {
          if (current.value.status !== 'uncertain')
            set('unavailable', current.value.generation, null);
          throw error;
        }
      }
      const operation = begin(kind);
      try {
        if (all) await api.logoutAll();
        else await api.logout();
      } catch (error) {
        finish(operation, 'uncertain');
        set('uncertain', operation.generation, null);
        throw error;
      }
      finish(operation, 'settled');
      set('anonymous', operation.generation, null);
    });
  }
  function capture(): SessionGuard {
    available();
    if (current.value.status !== 'authenticated' || !current.value.subjectId)
      throw new GuardMismatchError();
    return { generation: current.value.generation, subjectId: current.value.subjectId };
  }
  async function guarded<T>(guard: SessionGuard, action: () => Promise<T>): Promise<T> {
    return available().lock('shared', async () => {
      assertGuard(guard, readJournal());
      const attempt = revision;
      const result = await action();
      if (attempt !== revision) throw new GuardMismatchError();
      assertGuard(guard, readJournal());
      return result;
    });
  }
  function guardedBook(guard: SessionGuard, action: () => Promise<AddressBook>) {
    return guarded(guard, async () => {
      const book = await action();
      assertGuard(guard, readJournal());
      if (subject(book.subjectId) !== guard.subjectId) rejectIdentity(guard.generation);
      rememberIdentity(guard.generation, subject(book.subjectId));
      return book;
    });
  }
  function guardedSettings(guard: SessionGuard, action: () => Promise<AccountSettings>) {
    return guarded(guard, async () => {
      const settings = await action();
      assertGuard(guard, readJournal());
      if (subject(settings.subjectId) !== guard.subjectId) rejectIdentity(guard.generation);
      rememberIdentity(guard.generation, subject(settings.subjectId));
      return settings;
    });
  }
  const unsubscribe =
    env?.subscribe((notice) => {
      if (disposed || (notice && notice.operationId === ownOperation)) return;
      if (notice?.phase === 'intent') {
        if (
          typeof notice.generation !== 'string' ||
          notice.generation.length > 128 ||
          !kinds.includes(notice.kind)
        )
          return;
        try {
          if ((readJournal()?.generation ?? '') !== notice.previousGeneration) return;
        } catch {
          return;
        }
        revision++;
        set(notice.kind === 'login' ? 'switching' : 'signingOut', notice.generation, null);
        return;
      }
      try {
        const journal = readJournal();
        if (!journal) {
          revision++;
          set('unknown', '', null);
          return;
        }
        if (journal.operationId === ownOperation) return;
        if (mismatchedGeneration === journal.generation) {
          set('uncertain', journal.generation, null);
          return;
        }
        if (
          journal.kind === 'refresh' &&
          journal.generation === current.value.generation &&
          current.value.subjectId &&
          journal.phase !== 'uncertain'
        ) {
          set(journal.phase === 'pending' ? 'checking' : 'authenticated');
          return;
        }
        if (
          journal.generation === current.value.generation &&
          journal.phase === 'settled' &&
          current.value.status === 'authenticated'
        )
          return;
        revision++;
        set(
          journal.phase === 'settled'
            ? journal.kind === 'logout' ||
              journal.kind === 'logoutAll' ||
              journal.kind === 'expired'
              ? 'anonymous'
              : 'unknown'
            : journal.phase === 'pending'
              ? 'checking'
              : 'uncertain',
          journal.generation,
          null,
        );
        if (
          journal.phase === 'settled' &&
          journal.kind !== 'logout' &&
          journal.kind !== 'logoutAll' &&
          journal.kind !== 'expired'
        )
          void bootstrap().then(() => {
            if (!disposed && current.value.status === 'unknown') void bootstrap();
          });
      } catch {
        /* readJournal already enters unsupported */
      }
    }) ?? (() => {});
  return {
    state: readonly(current),
    bootstrap,
    login,
    logout,
    capture,
    async register(identifier, password) {
      available();
      await api.register(identifier, password);
    },
    async readProfile(guard) {
      if (!guard && current.value.status !== 'authenticated') await bootstrap();
      if (!guard && current.value.status === 'profilePending') {
        const generation = current.value.generation;
        return available().lock('shared', async () => {
          const journal = readJournal();
          if (journal?.phase !== 'settled' || journal.generation !== generation)
            throw new GuardMismatchError();
          const profile = await probe(generation, true, true);
          if (!profile) throw new GuardMismatchError();
          return profile;
        });
      }
      const selected = guard ?? capture();
      const read = () =>
        guarded(selected, async () => {
          const profile = await api.getProfile();
          if (subject(profile.subjectId) !== selected.subjectId)
            rejectIdentity(selected.generation);
          rememberIdentity(selected.generation, subject(profile.subjectId));
          return profile;
        });
      try {
        return await read();
      } catch (error) {
        if (!unauthenticated(error)) throw error;
        // The shared lock has been released before bootstrap can request exclusive access.
        await bootstrap();
        return read();
      }
    },
    async readSettings(guard) {
      const read = () => guardedSettings(guard, () => api.getSettings());
      try {
        return await read();
      } catch (error) {
        if (!unauthenticated(error)) throw error;
        await bootstrap();
        return read();
      }
    },
    updateSettings: (input, guard) => guardedSettings(guard, () => api.updateSettings(input)),
    async readAddresses(guard) {
      const read = () => guardedBook(guard, () => api.listAddresses());
      try {
        return await read();
      } catch (error) {
        if (!unauthenticated(error)) throw error;
        await bootstrap();
        return read();
      }
    },
    createAddress: (input, guard) => guardedBook(guard, () => api.createAddress(input)),
    updateAddress: (input, guard) => guardedBook(guard, () => api.updateAddress(input)),
    deleteAddress: (input, guard) => guardedBook(guard, () => api.deleteAddress(input)),
    setDefaultAddress: (input, guard) => guardedBook(guard, () => api.setDefaultAddress(input)),
    updateProfile: (input, guard) =>
      guarded(guard, async () => {
        const profile = await api.updateProfile(input);
        if (subject(profile.subjectId) !== guard.subjectId) rejectIdentity(guard.generation);
        rememberIdentity(guard.generation, subject(profile.subjectId));
        return profile;
      }),
    dispose() {
      disposed = true;
      revision++;
      unsubscribe();
      env?.dispose?.();
      current.value = { status: 'unknown', generation: '', subjectId: null };
    },
  };
}
