# go-fragment

Неофициальный клиент [Fragment](https://fragment.com) на Go. Им ищут Telegram-получателя и покупают ему Stars или дарят Premium на 3, 6 или 12 месяцев. Оплату подписывает кошелёк вызывающего.

Fragment это API не публикует. Пакет не связан с витриной. GGSEL и другие кассы остаются снаружи: они сами передают имя пользователя, сумму и свой непрозрачный ключ идемпотентности. Вебхука и префикса ключа в модуле нет.

Лицензия MIT. Нужен Go 1.24.

## Граница

Вызывающий приносит сессию Fragment (`stel_ssid`, `stel_dt`, `stel_token`, `stel_ton_token`) и мнемонику горячего кошелька. Мнемоника не уходит из локального подписанта. Cookie уходят только на `fragment.com`. Toncenter и любой другой `Broadcaster` не получают ни cookie, ни мнемонику.

Основной кошелёк сюда не подключают. В 14.4.2 чужой выпуск `fragment-api-py` увёл 43.982037696 TON; смотрите [issue #8](https://github.com/aiexz/fragment-api-py/issues/8) того проекта. Этот модуль не копирует его скрытую комиссию 1.5%. Донат выключен, пока вызывающий сам не передаст `WithDonation`. Адрес из того инцидента не принимается ни как казна, ни как донат.

`Pay` тратит TON. `nil` и состояние `broadcast` значат, что транслятор принял сообщение, а не что Stars зачислены. `Pay` не вызывает `confirmReq`. Зачисление делает `Reconcile`: он один постит `confirmReq` и потом опрашивает state. `Resolve(settled)` без найденной в сети транзакции файл не меняет.

Две копии каталога журнала на двух машинах заплатят дважды. Журнал один на один процесс оплаты. Каталог по умолчанию `~/.go-fragment`, права `0700` / `0600`.

Казна Fragment не зашита. `WithFragmentTreasury` задаёт адреса, на которые разрешено платить. `WithSpendLimit` — один потолок в nanotons на операцию, его ставят на самый дорогой SKU. `Client.Balance` читает баланс горячего кошелька и не заменяет проверку внутри `Pay`.

`WithShowSender` по умолчанию `true`, как на сайте. `false` скрывает отправителя. Значение входит в идентичность заказа: другой флаг на том же ключе — конфликт, инвойс не пересобирается.

Отпечаток TLS стандартного `net/http` Fragment может не принять. Это не обещание, что рукопожатие проходит. Другой клиент передаётся через `WithHTTPDoer`.

Повтор того же ключа безопасен, пока строка не подписана заново. `invoice_expired` и `prepare_stuck` требуют новый ключ. `outcome_unknown` не означает неудачу перевода и не повод для возврата денег без сверки. Подписанный, но не отправленный BOC шлёт только явный `Resume`.

## English

Unofficial local Fragment client for Telegram Stars and Premium gifts. The caller supplies the session, the hot wallet, and an opaque idempotency key. There is no storefront adapter and no silent fee. `Pay` spends TON. A nil error at `broadcast` is not delivery. Do not point two hosts at two copies of the journal. Do not connect a primary wallet.

Specification: [DESIGN.md](DESIGN.md). Example: [example/stars](example/stars).
