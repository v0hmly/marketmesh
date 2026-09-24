import { Code, ConnectError } from '@connectrpc/connect';

export { createSecurityApi } from './client';

export function securityError(error: unknown): string {
  if (!(error instanceof ConnectError))
    return 'Результат запроса не подтверждён. Обновите данные перед новой попыткой.';
  switch (error.code) {
    case Code.InvalidArgument:
      return 'Данные не прошли проверку. Проверьте пароль, адрес или код из последнего письма.';
    case Code.Unauthenticated:
      return 'Сеанс или пароль не подтверждён. Проверьте пароль либо войдите снова.';
    case Code.FailedPrecondition:
      return 'Действие сейчас недоступно. Проверьте срок ссылки, новое письмо и ограничения аккаунта.';
    case Code.ResourceExhausted:
      return 'Лимит запросов исчерпан. Повторите попытку через 15 минут.';
    case Code.NotFound:
      return 'Запись уже недоступна. Обновите данные аккаунта.';
    case Code.Unimplemented:
      return 'Сервис безопасности временно недоступен. Попробуйте позже.';
    default:
      return 'Результат запроса не подтверждён. Обновите данные перед новой попыткой.';
  }
}
