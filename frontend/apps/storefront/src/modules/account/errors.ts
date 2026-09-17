import { Code, ConnectError } from '@connectrpc/connect';

export function accountError(error: unknown): string {
  if (error instanceof ConnectError) {
    switch (error.code) {
      case Code.InvalidArgument:
        return 'Проверьте введённые данные. Сервер не принял одно из значений.';
      case Code.Unauthenticated:
        return 'Не удалось подтвердить вход. Проверьте логин и пароль или войдите заново.';
      case Code.PermissionDenied:
        return 'Недостаточно прав для этого действия.';
      case Code.ResourceExhausted:
        return 'Слишком много запросов. Подождите немного и повторите действие.';
      case Code.Unavailable:
        return 'Сервис временно недоступен. Попробуйте позже.';
      case Code.DeadlineExceeded:
        return 'Ответ не получен вовремя. Результат операции может быть неизвестен.';
      case Code.Aborted:
        return 'Профиль изменился в другом окне. Перечитайте актуальные данные перед сохранением.';
    }
  }
  return 'Не удалось завершить действие. Проверьте соединение и попробуйте снова.';
}
