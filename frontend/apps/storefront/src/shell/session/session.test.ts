import { describe, expect, it, vi } from 'vitest';
import { Code, ConnectError } from '@connectrpc/connect';
import type { PublicApi, Profile, AddressBook } from '../../shared/api/types';
import {
  createSessionController,
  GuardMismatchError,
  SessionUnavailableError,
  type SessionEnvironment,
  type SessionJournal,
  type SessionNotice,
} from './index';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
const profile = (id = 1): Profile => ({
  $typeName: 'user.v1.Profile',
  subjectId: new Uint8Array(16).fill(id),
  displayName: 'Private name',
  bio: 'Private biography',
  version: 1n,
  createdAtUnix: 1n,
  updatedAtUnix: 1n,
});
// A real wire detail exercises shared/api parsing without importing internal schemas.
const pendingDetail = () => {
  const field = (tag: number, text: string) => {
    const bytes = new TextEncoder().encode(text);
    return [tag, bytes.length, ...bytes];
  };
  return {
    type: 'google.rpc.ErrorInfo',
    value: Uint8Array.from([...field(10, 'PROFILE_NOT_READY'), ...field(18, 'marketmesh.user')]),
  };
};
const denied = () => new ConnectError('authentication failed', Code.Unauthenticated);
function api(): PublicApi {
  return {
    register: vi.fn(async () => {}),
    login: vi.fn(async () => profile().subjectId),
    refresh: vi.fn(async () => {}),
    logout: vi.fn(async () => {}),
    logoutAll: vi.fn(async () => {}),
    getSettings: vi.fn(),
    updateSettings: vi.fn(),
    listAddresses: vi.fn(),
    createAddress: vi.fn(),
    updateAddress: vi.fn(),
    deleteAddress: vi.fn(),
    setDefaultAddress: vi.fn(),
    getProfile: vi.fn(async () => profile()),
    updateProfile: vi.fn(async () => profile()),
  };
}
/** FIFO lock manager with concurrent shared holders and exclusive writer exclusion. */
function tabs() {
  let journal: SessionJournal | null = null;
  let serial = 0;
  let shared = 0;
  let exclusive = false;
  const listeners = new Map<string, (notice?: SessionNotice) => void>();
  const queue: Array<{ mode: 'shared' | 'exclusive'; run: () => void }> = [];
  function drain() {
    if (exclusive) return;
    while (queue.length) {
      const item = queue[0]!;
      if (item.mode === 'exclusive' && shared) return;
      queue.shift();
      if (item.mode === 'exclusive') exclusive = true;
      else shared++;
      item.run();
      if (exclusive) return;
    }
  }
  function send(source: string, notice?: SessionNotice) {
    for (const [id, listener] of listeners)
      if (id !== source) queueMicrotask(() => listener(notice));
  }
  return {
    get journal() {
      return journal;
    },
    set journal(value: SessionJournal | null) {
      journal = value;
    },
    environment(): SessionEnvironment {
      const id = `tab-${++serial}`;
      return {
        randomId: () => `opaque-${++serial}`,
        read: () => journal,
        write: (value) => {
          journal = { ...value };
          send(id);
        },
        publish: (value) => send(id, value),
        subscribe: (listener) => {
          listeners.set(id, listener);
          return () => {
            listeners.delete(id);
          };
        },
        lock: <T>(mode: 'shared' | 'exclusive', work: () => Promise<T>) =>
          new Promise<T>((resolve, reject) => {
            queue.push({
              mode,
              run: () => {
                void work()
                  .then(resolve, reject)
                  .finally(() => {
                    if (mode === 'exclusive') exclusive = false;
                    else shared--;
                    drain();
                  });
              },
            });
            drain();
          }),
      };
    },
  };
}
const settle = async () => {
  for (let i = 0; i < 20; i++) await Promise.resolve();
};

describe('shell session coordination', () => {
  it('single-flights bootstrap and refresh across two tabs, rechecking after exclusive acquisition', async () => {
    const shared = tabs();
    const backend = api();
    let fresh = false;
    vi.mocked(backend.getProfile).mockImplementation(async () => {
      if (!fresh) throw denied();
      return profile();
    });
    vi.mocked(backend.refresh).mockImplementation(async () => {
      fresh = true;
    });
    const a = createSessionController(backend, { environment: shared.environment() });
    const b = createSessionController(backend, { environment: shared.environment() });
    await Promise.all([a.bootstrap(), a.bootstrap(), b.bootstrap(), b.bootstrap()]);
    await settle();
    expect(backend.refresh).toHaveBeenCalledTimes(1);
    expect(a.state.value.status).toBe('authenticated');
    expect(b.state.value.status).toBe('authenticated');
    const formOwner = a.capture();
    const otherTabOwner = b.capture();
    fresh = false;
    await a.readProfile();
    await settle();
    expect(backend.refresh).toHaveBeenCalledTimes(2);
    expect(a.capture()).toEqual(formOwner);
    expect(b.capture()).toEqual(otherTabOwner);
    // A form keyed by owner+generation survives ordinary same-owner rotation.
    await expect(
      a.updateProfile({ displayName: 'unsaved draft', bio: '', expectedVersion: 1n }, formOwner),
    ).resolves.toEqual(profile());
    a.dispose();
    b.dispose();
  });

  it('does not replay a refresh whose reply was lost, including a new tab', async () => {
    const shared = tabs();
    const backend = api();
    vi.mocked(backend.getProfile).mockRejectedValue(denied());
    vi.mocked(backend.refresh).mockRejectedValue(
      new ConnectError('unknown outcome', Code.Unavailable),
    );
    const a = createSessionController(backend, { environment: shared.environment() });
    await a.bootstrap();
    expect(a.state.value.status).toBe('uncertain');
    expect(shared.journal?.phase).toBe('uncertain');
    a.dispose();
    const b = createSessionController(backend, { environment: shared.environment() });
    await b.bootstrap();
    await b.bootstrap();
    expect(backend.refresh).toHaveBeenCalledTimes(1);
    expect(b.state.value.status).toBe('uncertain');
    b.dispose();
  });

  it('treats an orphaned pending operation as uncertain without refresh', async () => {
    const shared = tabs();
    shared.journal = {
      generation: 'crashed',
      operationId: 'opaque',
      kind: 'refresh',
      phase: 'pending',
    };
    const backend = api();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    expect(controller.state.value.status).toBe('uncertain');
    expect(backend.refresh).not.toHaveBeenCalled();
    controller.dispose();
  });

  it('serializes logout behind refresh and never restores private state from the old result', async () => {
    const shared = tabs();
    const backend = api();
    const rotate = deferred<void>();
    let fresh = false;
    vi.mocked(backend.getProfile).mockImplementation(async () => {
      if (!fresh) throw denied();
      return profile();
    });
    vi.mocked(backend.refresh).mockImplementation(async () => {
      await rotate.promise;
      fresh = true;
    });
    const a = createSessionController(backend, { environment: shared.environment() });
    const b = createSessionController(backend, { environment: shared.environment() });
    const boot = a.bootstrap();
    await vi.waitFor(() => expect(backend.refresh).toHaveBeenCalledTimes(1));
    const logout = b.logout();
    expect(b.state.value.status).toBe('signingOut');
    expect(backend.logout).not.toHaveBeenCalled();
    rotate.resolve();
    await Promise.all([boot, logout]);
    await settle();
    expect(backend.logout).toHaveBeenCalledTimes(1);
    expect(a.state.value.status).toBe('anonymous');
    expect(b.state.value.status).toBe('anonymous');
    a.dispose();
    b.dispose();
  });

  it('rejects a form captured for A after login to B in another tab', async () => {
    const shared = tabs();
    const backend = api();
    let owner = 1;
    vi.mocked(backend.getProfile).mockImplementation(async () => profile(owner));
    vi.mocked(backend.login).mockImplementation(async () => {
      owner = 2;
      return profile(2).subjectId;
    });
    const a = createSessionController(backend, { environment: shared.environment() });
    const b = createSessionController(backend, { environment: shared.environment() });
    await a.bootstrap();
    const guard = a.capture();
    await b.login('B', new Uint8Array([1]));
    await settle();
    await expect(
      a.updateProfile({ displayName: 'A draft', bio: '', expectedVersion: 1n }, guard),
    ).rejects.toBeInstanceOf(GuardMismatchError);
    expect(backend.updateProfile).not.toHaveBeenCalled();
    a.dispose();
    b.dispose();
  });

  it('rejects a late read after logout intent instead of exposing its private result', async () => {
    const shared = tabs();
    const backend = api();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const pending = deferred<ReturnType<typeof profile>>();
    vi.mocked(backend.getProfile).mockImplementationOnce(() => pending.promise);
    const read = controller.readProfile();
    const rejected = expect(read).rejects.toBeInstanceOf(GuardMismatchError);
    await settle();
    const logout = controller.logout();
    pending.resolve(profile());
    await rejected;
    await logout;
    expect(controller.state.value.status).toBe('anonymous');
    controller.dispose();
  });

  it('never automatically retries profile mutations after authentication or transport errors', async () => {
    const shared = tabs();
    const backend = api();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const guard = controller.capture();
    vi.mocked(backend.updateProfile)
      .mockRejectedValueOnce(denied())
      .mockRejectedValueOnce(new ConnectError('unknown commit', Code.Unavailable));
    const input = { displayName: 'draft', bio: '', expectedVersion: 1n };
    await expect(controller.updateProfile(input, guard)).rejects.toBeInstanceOf(ConnectError);
    await expect(controller.updateProfile(input, guard)).rejects.toBeInstanceOf(ConnectError);
    expect(backend.updateProfile).toHaveBeenCalledTimes(2);
    expect(backend.refresh).not.toHaveBeenCalled();
    controller.dispose();
  });

  it('recognizes only typed PROFILE_NOT_READY as an authenticated pending profile', async () => {
    const shared = tabs();
    const backend = api();
    const pending = new ConnectError('profile not ready', Code.NotFound);
    pending.details.push(pendingDetail());
    vi.mocked(backend.getProfile).mockRejectedValue(pending);
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    expect(controller.state.value.status).toBe('profilePending');
    expect(backend.refresh).not.toHaveBeenCalled();
    await expect(controller.readProfile()).rejects.toBe(pending);
    vi.mocked(backend.getProfile).mockRejectedValue(
      new ConnectError('PROFILE_NOT_READY', Code.NotFound),
    );
    await controller.bootstrap();
    expect(controller.state.value.status).toBe('unavailable');
    vi.mocked(backend.getProfile).mockRejectedValueOnce(pending);
    await controller.bootstrap();
    expect(controller.state.value.status).toBe('profilePending');
    vi.mocked(backend.getProfile).mockResolvedValue(profile());
    await expect(controller.readProfile()).resolves.toEqual(profile());
    expect(controller.state.value.status).toBe('authenticated');
    controller.dispose();
  });

  it('does not claim revocation when logout outcome is unknown', async () => {
    const shared = tabs();
    const backend = api();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    vi.mocked(backend.logoutAll).mockRejectedValue(
      new ConnectError('unavailable', Code.Unavailable),
    );
    await expect(controller.logout(true)).rejects.toBeInstanceOf(ConnectError);
    expect(controller.state.value.status).toBe('uncertain');
    expect(controller.state.value.subjectId).toBeNull();
    expect(shared.journal?.phase).toBe('uncertain');
    controller.dispose();
  });

  it('fails closed without safe coordination and stores no identity, credentials or profile fields', async () => {
    const backend = api();
    const unsupported = createSessionController(backend, { environment: null });
    expect(unsupported.state.value.status).toBe('unsupported');
    await expect(
      unsupported.login('secret-identifier', new Uint8Array([99])),
    ).rejects.toBeInstanceOf(SessionUnavailableError);
    expect(backend.login).not.toHaveBeenCalled();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.login('secret-identifier', new Uint8Array([99]));
    expect(Object.keys(shared.journal!).sort()).toEqual([
      'generation',
      'kind',
      'operationId',
      'phase',
    ]);
    expect(JSON.stringify(shared.journal)).not.toMatch(
      /secret-identifier|Private name|Private biography|subjectId/,
    );
    expect(Object.keys(controller.state.value).sort()).toEqual([
      'generation',
      'status',
      'subjectId',
    ]);
    controller.dispose();
    unsupported.dispose();
  });
  it('does not send a session mutation when its durable pending journal cannot be written', async () => {
    const shared = tabs();
    const backend = api();
    const environment = shared.environment();
    environment.write = () => {
      throw new Error('storage denied');
    };
    const controller = createSessionController(backend, { environment });
    await expect(controller.login('owner', new Uint8Array([1]))).rejects.toBeInstanceOf(
      SessionUnavailableError,
    );
    expect(backend.login).not.toHaveBeenCalled();
    expect(controller.state.value.status).toBe('unsupported');
    controller.dispose();
  });

  it('restores expired access once before logout and keeps the durable result anonymous on reload', async () => {
    const shared = tabs();
    const backend = api();
    let valid = true;
    vi.mocked(backend.getProfile).mockImplementation(async () => {
      if (!valid) throw denied();
      return profile();
    });
    vi.mocked(backend.refresh).mockImplementation(async () => {
      valid = true;
    });
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    valid = false;
    await controller.logout();
    expect(backend.refresh).toHaveBeenCalledTimes(1);
    expect(backend.logout).toHaveBeenCalledTimes(1);
    controller.dispose();
    const reloaded = createSessionController(backend, { environment: shared.environment() });
    await reloaded.bootstrap();
    expect(reloaded.state.value.status).toBe('anonymous');
    expect(backend.refresh).toHaveBeenCalledTimes(1);
    reloaded.dispose();
  });

  it.each(['recovery', 'read'] as const)(
    'fails closed on same-generation owner changes via %s until explicit login',
    async (mode) => {
      const shared = tabs();
      const backend = api();
      const controller = createSessionController(backend, { environment: shared.environment() });
      await controller.bootstrap();
      const oldGuard = controller.capture();
      if (mode === 'recovery') {
        vi.mocked(backend.getProfile).mockRejectedValueOnce(
          new ConnectError('outage', Code.Unavailable),
        );
        await controller.bootstrap();
        expect(controller.state.value.status).toBe('unavailable');
        expect(controller.state.value.subjectId).toBeNull();
        vi.mocked(backend.getProfile).mockResolvedValue(profile(2));
        await controller.bootstrap();
      } else {
        vi.mocked(backend.getProfile).mockResolvedValue(profile(2));
        await expect(controller.readProfile()).rejects.toBeInstanceOf(GuardMismatchError);
      }
      expect(controller.state.value.status).toBe('uncertain');
      expect(controller.state.value.subjectId).toBeNull();
      expect(() => controller.capture()).toThrow(GuardMismatchError);
      const calls = vi.mocked(backend.getProfile).mock.calls.length;
      await controller.bootstrap();
      expect(backend.getProfile).toHaveBeenCalledTimes(calls);
      await expect(
        controller.updateProfile(
          { displayName: 'A draft', bio: '', expectedVersion: 1n },
          oldGuard,
        ),
      ).rejects.toBeInstanceOf(GuardMismatchError);
      expect(backend.updateProfile).not.toHaveBeenCalled();
      vi.mocked(backend.login).mockResolvedValue(profile(2).subjectId);
      await controller.login('explicit-B', new Uint8Array([1]));
      expect(controller.state.value.status).toBe('authenticated');
      expect(controller.capture().generation).not.toBe(oldGuard.generation);
      controller.dispose();
    },
  );

  it.each([
    { gap: 'after initialization', interceptedMode: 'exclusive', uncertain: false },
    { gap: 'before shared acquisition', interceptedMode: 'shared', uncertain: true },
  ] as const)(
    'classifies refresh state under lock $gap',
    async ({ interceptedMode, uncertain }) => {
      const shared = tabs();
      const backend = api();
      let fresh = true;
      const entered = deferred<void>();
      const release = deferred<void>();
      vi.mocked(backend.getProfile).mockImplementation(async () => {
        if (!fresh) throw denied();
        return profile();
      });
      vi.mocked(backend.refresh).mockImplementation(async () => {
        entered.resolve();
        await release.promise;
        fresh = true;
        if (uncertain) throw new ConnectError('rotation response lost', Code.Unavailable);
      });
      const a = createSessionController(backend, { environment: shared.environment() });
      const environment = shared.environment();
      const underlyingLock = environment.lock;
      let intercept = false;
      let recovery: Promise<unknown> | undefined;
      environment.lock = async <T>(
        mode: 'shared' | 'exclusive',
        work: () => Promise<T>,
      ): Promise<T> => {
        if (!intercept || mode !== interceptedMode) return underlyingLock(mode, work);
        intercept = false;
        if (mode === 'exclusive') {
          // The initialize lock has genuinely been released before A starts its refresh.
          const initialized = await underlyingLock(mode, work);
          recovery = a.readProfile().catch(() => undefined);
          await entered.promise;
          return initialized;
        }
        // B has finished initialize, but has not acquired its shared probe lock yet.
        recovery = a.readProfile().catch(() => undefined);
        await entered.promise;
        return underlyingLock(mode, work);
      };
      const b = createSessionController(backend, { environment });
      await Promise.all([a.bootstrap(), b.bootstrap()]);
      await settle();
      const owner = b.capture();
      fresh = false;
      intercept = true;
      const boot = b.bootstrap();
      await entered.promise;
      await settle();
      expect(b.state.value.status).not.toBe('uncertain');
      expect(b.state.value.generation).toBe(owner.generation);
      expect(b.state.value.subjectId).toBe(owner.subjectId);
      const readsBeforeCompletion = vi.mocked(backend.getProfile).mock.calls.length;
      release.resolve();
      await Promise.all([boot, recovery]);
      await settle();
      if (uncertain) {
        expect(b.state.value.status).toBe('uncertain');
        expect(b.state.value.subjectId).toBeNull();
        expect(backend.getProfile).toHaveBeenCalledTimes(readsBeforeCompletion);
      } else {
        expect(b.state.value.status).toBe('authenticated');
        expect(b.capture()).toEqual(owner);
      }
      expect(backend.refresh).toHaveBeenCalledTimes(1);
      a.dispose();
      b.dispose();
    },
  );
});

const book = (id = 1): AddressBook => ({
  $typeName: 'user.v1.AddressBook',
  subjectId: new Uint8Array(16).fill(id),
  version: 1n,
  addresses: [],
});
describe('address session isolation', () => {
  it('guards every address mutation before network and never retries unknown outcomes', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const owner = controller.capture();
    const input = {
      expectedBookVersion: 1n,
      addressId: new Uint8Array(16).fill(1),
      fields: {
        recipient: '',
        phone: '',
        country: '',
        postalCode: '',
        city: '',
        streetHouse: '',
        apartment: '',
        comment: '',
      },
    };
    for (const method of [
      'createAddress',
      'updateAddress',
      'deleteAddress',
      'setDefaultAddress',
    ] as const) {
      vi.mocked(backend[method]).mockRejectedValueOnce(denied());
      await expect(controller[method](input, owner)).rejects.toMatchObject({
        code: Code.Unauthenticated,
      });
      expect(backend[method]).toHaveBeenCalledTimes(1);
    }
    expect(backend.refresh).not.toHaveBeenCalled();
    await controller.logout();
    for (const method of [
      'createAddress',
      'updateAddress',
      'deleteAddress',
      'setDefaultAddress',
    ] as const) {
      await expect(controller[method](input, owner)).rejects.toBeInstanceOf(GuardMismatchError);
      expect(backend[method]).toHaveBeenCalledTimes(1);
    }
    controller.dispose();
  });
  it('rejects a wrong book owner and keeps the generation invalidated', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    vi.mocked(backend.listAddresses).mockResolvedValue(book(2));
    await expect(controller.readAddresses(controller.capture())).rejects.toBeInstanceOf(
      GuardMismatchError,
    );
    expect(controller.state.value.status).toBe('uncertain');
    await controller.bootstrap();
    expect(controller.state.value.status).toBe('uncertain');
    controller.dispose();
  });
  it('holds address requests under a shared lock and rejects their reply after logout intent', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const pending = deferred<ReturnType<typeof book>>();
    vi.mocked(backend.listAddresses).mockReturnValue(pending.promise);
    const reading = controller.readAddresses(controller.capture());
    const rejected = expect(reading).rejects.toBeInstanceOf(GuardMismatchError);
    await settle();
    const logout = controller.logout();
    await settle();
    expect(backend.logout).not.toHaveBeenCalled();
    pending.resolve(book());
    await rejected;
    await logout;
    expect(controller.state.value.status).toBe('anonymous');
    controller.dispose();
  });
  it('recovers authentication only for a read and rechecks its original owner', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    vi.mocked(backend.listAddresses).mockRejectedValueOnce(denied()).mockResolvedValue(book());
    await expect(controller.readAddresses(controller.capture())).resolves.toEqual(book());
    expect(backend.listAddresses).toHaveBeenCalledTimes(2);
    controller.dispose();
  });
});

describe('settings session isolation', () => {
  it('checks the owner, holds the session lock and discards late settings after logout intent', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const gate = deferred<{ subjectId: Uint8Array; version: bigint; theme: 'dark' }>();
    vi.mocked(backend.getSettings).mockReturnValue(gate.promise);
    const read = controller.readSettings(controller.capture());
    const rejected = expect(read).rejects.toBeInstanceOf(GuardMismatchError);
    await settle();
    const logout = controller.logout();
    await settle();
    expect(backend.logout).not.toHaveBeenCalled();
    gate.resolve({ subjectId: profile().subjectId, version: 1n, theme: 'dark' });
    await rejected;
    await logout;
    expect(controller.state.value.status).toBe('anonymous');
    controller.dispose();
  });
  it('rejects another owner in settings and invalidates the generation', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    vi.mocked(backend.getSettings).mockResolvedValue({
      subjectId: profile(2).subjectId,
      version: 1n,
      theme: 'dark',
    });
    await expect(controller.readSettings(controller.capture())).rejects.toBeInstanceOf(
      GuardMismatchError,
    );
    expect(controller.state.value.status).toBe('uncertain');
    controller.dispose();
  });
  it('never retries a settings mutation and rejects an old form before network', async () => {
    const backend = api();
    const shared = tabs();
    const controller = createSessionController(backend, { environment: shared.environment() });
    await controller.bootstrap();
    const guard = controller.capture();
    vi.mocked(backend.updateSettings).mockRejectedValueOnce(denied());
    await expect(
      controller.updateSettings({ theme: 'dark', expectedVersion: 1n }, guard),
    ).rejects.toMatchObject({ code: Code.Unauthenticated });
    expect(backend.refresh).not.toHaveBeenCalled();
    await controller.logout();
    await expect(
      controller.updateSettings({ theme: 'dark', expectedVersion: 1n }, guard),
    ).rejects.toBeInstanceOf(GuardMismatchError);
    expect(backend.updateSettings).toHaveBeenCalledTimes(1);
    controller.dispose();
  });
});
