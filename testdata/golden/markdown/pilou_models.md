# Model Duplication Analysis Report

- Analysis date: 2026-03-17
- Scope: pilou-bo-api, pilou-app-api, onboarding-webapp database models only, using main/master branch snapshots
- Branch snapshots used:
  - `onboarding-webapp`: master (remote `github/master`, analyzed via worktree at commit `f716e9b`)
  - `pilou-app-api`: main (`4084f2e`)
  - `pilou-bo-api`: main (`ff2e848`)

## Summary

- Total models found: **151**
  - `onboarding-webapp`: 79 (Prisma: main + legacy schemas)
  - `pilou-bo-api`: 69 (Django models + unmanaged wrappers)
  - `pilou-app-api`: 3 (Django models)
- Exact table-name duplicate groups across repos: **22**
- High-confidence semantic duplicates with different table names: **2**
- Highest-risk collision for one-schema migration: `movements` table means different things across systems (`transactions movements` vs `legacy portfolio movements`).

## Exact Duplicate Tables Across Repositories

| Effective table | Models | Normalized field overlap | Confidence |
|---|---|---:|---|
| `Address` | `pilou-bo-api:PilouAddress`; `onboarding-webapp:Address` | 0.86 | High |
| `balances` | `pilou-app-api:Balances`; `pilou-bo-api:Balances` | 1.00 | High |
| `BankAccount` | `pilou-bo-api:Bankaccount`; `onboarding-webapp:BankAccount` | 0.86 | High |
| `Beneficiary` | `pilou-bo-api:Beneficiary`; `onboarding-webapp:Beneficiary` | 0.84 | High |
| `brockerage_houses` | `pilou-bo-api:BrockerageHouses`; `onboarding-webapp:brockerage_houses` | 0.80 | High |
| `Contract` | `pilou-bo-api:PilouContract`; `onboarding-webapp:Contract` | 0.92 | High |
| `contracts` | `pilou-app-api:Contract`; `pilou-bo-api:Contract` | 0.11 | Low |
| `CSF` | `pilou-bo-api:Csf`; `onboarding-webapp:CSF` | 0.77 | High |
| `EconomicActivity` | `pilou-bo-api:Economicactivity`; `onboarding-webapp:EconomicActivity` | 0.88 | High |
| `friend_codes` | `pilou-bo-api:FriendCodes`; `onboarding-webapp:friend_codes` | 0.57 | Medium |
| `investor_profiles` | `pilou-bo-api:InvestorProfiles`; `onboarding-webapp:investor_profiles` | 0.75 | High |
| `InvestorProfile` | `pilou-bo-api:Investorprofile`; `onboarding-webapp:InvestorProfile` | 0.84 | High |
| `Kyc` | `pilou-bo-api:PilouKyc`; `onboarding-webapp:Kyc` | 0.81 | High |
| `kycs` | `pilou-bo-api:Kycs`; `onboarding-webapp:kycs` | 0.52 | Medium |
| `movements` | `pilou-app-api:Movements`; `pilou-bo-api:Movements`; `onboarding-webapp:movements` | 0.07..1.00 | Conflict |
| `Pep` | `pilou-bo-api:Pep`; `onboarding-webapp:Pep` | 0.78 | High |
| `QuizAnswer` | `pilou-bo-api:Quizanswer`; `onboarding-webapp:QuizAnswer` | 0.40 | Medium |
| `QuizQuestion` | `pilou-bo-api:Quizquestion`; `onboarding-webapp:QuizQuestion` | 0.57 | Medium |
| `User` | `pilou-bo-api:PilouUser`; `onboarding-webapp:User` | 0.74 | Medium |
| `user_brockerage_houses` | `pilou-bo-api:UserBrockerageHouses`; `onboarding-webapp:user_brockerage_houses` | 0.45 | Medium |
| `UserBrokerAccount` | `pilou-bo-api:UserBrokerAccount`; `onboarding-webapp:UserBrokerAccount` | 0.38 | Low |
| `users` | `pilou-bo-api:LegacyUser`; `onboarding-webapp:users` | 0.42 | Medium |

### Notes on Critical Groups

- `balances`: `pilou-app-api` and `pilou-bo-api` are structurally identical (1.00 overlap).
- `movements`: `pilou-app-api` and `pilou-bo-api` are identical (1.00), but `onboarding-webapp:movements` is a different legacy portfolio model (0.07 overlap vs app/bo).
- `contracts`: both app and BO use table `contracts` but with very different shape (0.11 overlap) and duplicated AUM logic.
- `Contract`, `Kyc`, `User`, `InvestorProfile`, `Address`, `Beneficiary`, `BankAccount`, `CSF`, `EconomicActivity`, `Pep`: BO `pilou_models.py` wrappers are largely a mirror of onboarding Prisma tables (0.74-0.92 overlap).
- `users` and `kycs` are legacy-domain mirrors between BO and onboarding legacy schema (medium overlap due relations/adapter differences).

## High-Confidence Semantic Duplicates (Different Table Names)

| Pair | Field overlap | Why it matters |
|---|---:|---|
| `pilou-app-api:Movements[movements]` ↔ `onboarding-webapp:gbm_movements[gbm_movements]` | 0.96 | Same transactional domain, different naming convention/prefix (`gbm_`). |
| `pilou-app-api:Balances[balances]` ↔ `onboarding-webapp:gbm_balances[gbm_balances]` | 0.94 | Same transactional domain, different naming convention/prefix (`gbm_`). |

## Duplicated Model Logic

| Topic | Models | Evidence |
|---|---|---|
| AUM computation duplicated in `contracts` domain | `pilou-app-api.Contract, pilou-bo-api.Contract` | Both expose `get_aum`; app API delegates to `contract_service`, BO API computes directly with `Balances` query + funding-state logic. (`pilou-app-api/src/api/models/contract.py, pilou-app-api/src/api/services/contract_service.py, pilou-bo-api/src/api/models/contract.py`) |
| Movement retrieval logic split across model/service | `pilou-app-api.Contract (+ contract_service), pilou-bo-api.Contract` | App API exposes `get_movements` and `get_balances` from service; BO API has `get_amount_by_ticker` and `update_funded` using similar data source (transactions DB). (`pilou-app-api/src/api/services/contract_service.py, pilou-bo-api/src/api/models/contract.py`) |

## Proposed Migration Path (One Physical DB, One Schema)

1. Create a canonical model contract document before moving data.
   - Define canonical table names in **snake_case** for the unified schema (avoid quoted CamelCase table names like `User`, `Kyc`, `Address` long-term).
   - Mark each duplicate group as `canonical`, `compat adapter`, or `retire`.
2. Resolve conflicting names first (highest risk).
   - Split `movements` into explicit domains in the target schema (for example `transactions_movements` vs `portfolio_movements`) before any merge.
   - Split `contracts` semantics: BO `contracts` entity vs onboarding `Contract` document workflow table.
3. Consolidate exact mirrors into single physical tables with compatibility views.
   - Keep one canonical table for each mirrored Prisma/BO pair (`User`, `Kyc`, `InvestorProfile`, `Address`, `Beneficiary`, `BankAccount`, `CSF`, `EconomicActivity`, `Pep`, `Contract`).
   - Replace BO unmanaged wrappers in `pilou_models.py` with a shared client/repository layer over canonical tables.
4. Unify transactional models and logic.
   - Merge `Balances` and `Movements` models used by app+BO into one shared data-access package.
   - Move AUM/balance/movement read logic from model methods into a single service module consumed by both APIs.
5. Execute migration in dual-read/dual-write phases.
   - Phase A: backfill canonical tables from current sources.
   - Phase B: dual-write from BO/app/onboarding to canonical tables.
   - Phase C: cut reads to canonical views; monitor parity checks.
   - Phase D: retire legacy tables/adapters once parity is stable.
6. Add automated parity checks per duplicate group.
   - Row counts, key-field checksum, and business metric parity (`AUM`, deposit counts) between old and canonical tables during rollout.

## Suggested Canonicalization Priority

- **P0 (do first):** `movements`, `balances`, `contracts` logic merge, and `gbm_*` naming conflict resolution.
- **P1:** Prisma mirror tables in BO (`User`, `Kyc`, `InvestorProfile`, `Address`, `Beneficiary`, `BankAccount`, `CSF`, `EconomicActivity`, `Pep`, `Contract`).
- **P2:** Legacy mirror tables (`users`, `kycs`, `investor_profiles`, `friend_codes`, `user_brockerage_houses`).
- **P3:** Medium-overlap join/quiz/account groups (`QuizAnswer`, `QuizQuestion`, `UserBrokerAccount`).

## Draft Canonical Target Schema Map (Old Table -> New Table/View)

This is a first-pass target map for one physical DB and one logical schema.  
Naming convention in this draft: snake_case canonical physical tables + compatibility views for legacy names during transition.

| Old table(s) | Proposed canonical physical table | Compatibility view(s) to keep temporarily | Migration action |
|---|---|---|---|
| `balances` (`pilou-app-api`, `pilou-bo-api`), `gbm_balances` (`onboarding-webapp` legacy) | `tx_balances` | `balances`, `gbm_balances` | Merge data model; backfill and dual-write until parity |
| `movements` (`pilou-app-api`, `pilou-bo-api` transactions), `gbm_movements` (`onboarding-webapp` legacy) | `tx_movements` | `movements_tx`, `gbm_movements` | Merge transactional movement domain |
| `movements` (`onboarding-webapp` legacy portfolio model) | `legacy_portfolio_movements` | `movements_legacy_portfolio` | Split from transactional `movements` meaning before cutover |
| `contracts` (`pilou-app-api`, `pilou-bo-api`) | `core_contracts` | `contracts` | Keep contract/account domain here; move shared logic to service layer |
| `Contract` (`onboarding-webapp`), `Contract` (`pilou-bo-api` unmanaged wrapper `PilouContract`) | `onboarding_contract_documents` | `"Contract"` | Separate from `core_contracts`; this is onboarding document workflow |
| `User` (`onboarding-webapp`), `User` (`pilou-bo-api` unmanaged wrapper `PilouUser`) | `core_users` | `"User"` | Merge Prisma + BO mirror; replace BO unmanaged wrapper |
| `Kyc` (`onboarding-webapp`), `Kyc` (`pilou-bo-api` unmanaged wrapper `PilouKyc`) | `core_kyc_profiles` | `"Kyc"` | Merge Prisma + BO mirror; keep canonical naming in snake_case |
| `InvestorProfile` (`onboarding-webapp`), `InvestorProfile` (`pilou-bo-api` unmanaged wrapper `Investorprofile`) | `core_investor_profiles` | `"InvestorProfile"` | Merge Prisma + BO mirror |
| `Address` (`onboarding-webapp`), `Address` (`pilou-bo-api` unmanaged wrapper `PilouAddress`) | `core_addresses` | `"Address"` | Merge Prisma + BO mirror |
| `Beneficiary` (`onboarding-webapp`), `Beneficiary` (`pilou-bo-api`) | `core_beneficiaries` | `"Beneficiary"` | Merge Prisma + BO mirror |
| `BankAccount` (`onboarding-webapp`), `BankAccount` (`pilou-bo-api` unmanaged `Bankaccount`) | `core_bank_accounts` | `"BankAccount"` | Merge Prisma + BO mirror |
| `CSF` (`onboarding-webapp`), `CSF` (`pilou-bo-api` unmanaged `Csf`) | `core_csf_records` | `"CSF"` | Merge Prisma + BO mirror |
| `EconomicActivity` (`onboarding-webapp`), `EconomicActivity` (`pilou-bo-api` unmanaged `Economicactivity`) | `core_economic_activities` | `"EconomicActivity"` | Merge Prisma + BO mirror |
| `Pep` (`onboarding-webapp`), `Pep` (`pilou-bo-api`) | `core_pep_profiles` | `"Pep"` | Merge Prisma + BO mirror |
| `UserBrokerAccount` (`onboarding-webapp`), `UserBrokerAccount` (`pilou-bo-api`) | `core_user_broker_accounts` | `"UserBrokerAccount"` | Merge gradually; currently medium overlap, reconcile missing account fields first |
| `QuizQuestion` (`onboarding-webapp`), `QuizQuestion` (`pilou-bo-api`) | `core_quiz_questions` | `"QuizQuestion"` | Reconcile relation fields (`quizId`, reverse relations) then merge |
| `QuizAnswer` (`onboarding-webapp`), `QuizAnswer` (`pilou-bo-api`) | `core_quiz_answers` | `"QuizAnswer"` | Reconcile M2M/link fields before merge |
| `users` (`onboarding-webapp` legacy), `users` (`pilou-bo-api` legacy wrapper `LegacyUser`) | `legacy_users` | `users` | Keep as legacy domain until retirement plan completes |
| `kycs` (`onboarding-webapp` legacy), `kycs` (`pilou-bo-api` legacy wrapper `Kycs`) | `legacy_kycs` | `kycs` | Keep as legacy domain; map to `core_kyc_profiles` only after business signoff |
| `investor_profiles` (`onboarding-webapp` legacy), `investor_profiles` (`pilou-bo-api` legacy wrapper `InvestorProfiles`) | `legacy_investor_profiles` | `investor_profiles` | Legacy scoring profile domain; keep separate from `core_investor_profiles` |
| `friend_codes` (`onboarding-webapp` legacy), `friend_codes` (`pilou-bo-api` legacy wrapper `FriendCodes`) | `legacy_friend_codes` | `friend_codes` | Merge as legacy bounded context |
| `brockerage_houses` (`onboarding-webapp` legacy), `brockerage_houses` (`pilou-bo-api` legacy wrapper) | `legacy_brokerage_houses` | `brockerage_houses` | Canonical table fixes spelling; keep backward-compat view with old typo |
| `user_brockerage_houses` (`onboarding-webapp` legacy), `user_brockerage_houses` (`pilou-bo-api` legacy wrapper) | `legacy_user_brokerage_houses` | `user_brockerage_houses` | Canonical table fixes spelling; keep old view name temporarily |

### Draft Implementation Rules

1. For CamelCase legacy Prisma tables (`"User"`, `"Kyc"`, `"Address"`, etc.), create compatibility views after moving to snake_case physical tables.
2. For spelling-fix tables (`brockerage` -> `brokerage`), keep old typo as view name until all ORM mappings are updated.
3. Do not merge `core_*` and `legacy_*` domains in the same phase; stabilize each with parity checks first.
4. During cutover, point read paths to views first, then swap to canonical tables once code migrations are complete.

## Full Model Inventory

### pilou-app-api

| Model | Effective table | Managed | Source |
|---|---|---:|---|
| `Balances` | `balances` | no | `src/api/models/balance.py` |
| `Contract` | `contracts` | yes | `src/api/models/contract.py` |
| `Movements` | `movements` | no | `src/api/models/movement.py` |

### pilou-bo-api

| Model | Effective table | Managed | Source |
|---|---|---:|---|
| `Csftoquizanswer` | `_CSFToQuizAnswer` | no | `src/api/models/pilou_models.py` |
| `Economicactivitytoquizanswer` | `_EconomicActivityToQuizAnswer` | no | `src/api/models/pilou_models.py` |
| `Investorprofiletoquizanswer` | `_InvestorProfileToQuizAnswer` | no | `src/api/models/pilou_models.py` |
| `Kyctoquizanswer` | `_KycToQuizAnswer` | no | `src/api/models/pilou_models.py` |
| `Address` | `address` | yes | `src/api/models/address.py` |
| `PilouAddress` | `Address` | no | `src/api/models/pilou_models.py` |
| `Alert` | `alert` | yes | `src/api/models/alert.py` |
| `AlertEmailGbm` | `alert_email_gbm` | yes | `src/api/models/alert_email_gbm.py` |
| `Answer` | `api_answer` | yes | `src/api/models/catalogs/quiz.py` |
| `Blacklist` | `api_blacklist` | yes | `src/api/models/blacklist.py` |
| `Category` | `api_category` | yes | `src/api/models/catalogs/quiz.py` |
| `ClientReview` | `api_clientreview` | yes | `src/api/models/client_review.py` |
| `ClientRisk` | `api_clientrisk` | yes | `src/api/models/client_risk.py` |
| `CNBVRecord` | `api_cnbvrecord` | yes | `src/api/models/cnbv_record.py` |
| `EmailAddress` | `api_emailaddress` | yes | `src/api/models/email_address.py` |
| `Gender` | `api_gender` | yes | `src/api/models/catalogs/gender.py` |
| `InitialTransactionalProfile` | `api_initialtransactionalprofile` | yes | `src/api/models/initial_transactional_profile.py` |
| `Kyc` | `api_kyc` | yes | `src/api/models/kyc.py` |
| `Note` | `api_note` | yes | `src/api/models/note.py` |
| `OFACBlacklist` | `api_ofacblacklist` | yes | `src/api/models/ofac_blacklist.py` |
| `PilouUserStatus` | `api_pilouuserstatus` | yes | `src/api/models/pilou_user_status.py` |
| `Question` | `api_question` | yes | `src/api/models/catalogs/quiz.py` |
| `Quiz` | `api_quiz` | yes | `src/api/models/catalogs/quiz.py` |
| `RiskProfileHistory` | `api_riskprofilehistory` | yes | `src/api/models/contract.py` |
| `Balances` | `balances` | no | `src/api/models/balance.py` |
| `Bankaccount` | `BankAccount` | no | `src/api/models/pilou_models.py` |
| `Beneficiary` | `Beneficiary` | no | `src/api/models/pilou_models.py` |
| `Blog` | `blog` | yes | `src/api/models/blog.py` |
| `BlogCategory` | `blog_category` | yes | `src/api/models/catalogs/blog_category.py` |
| `BrockerageHouses` | `brockerage_houses` | no | `src/api/models/legacy.py` |
| `BalanceType` | `catalog_balance_type` | yes | `src/api/models/catalogs/balance_type.py` |
| `Lifecycle` | `catalog_lifecycle` | yes | `src/api/models/catalogs/lifecycle.py` |
| `CatalogOperationType` | `catalog_operation_type` | yes | `src/api/models/catalogs/operation_type.py` |
| `SocialAccountType` | `catalog_social_account_type` | yes | `src/api/models/catalogs/social_account.py` |
| `ContractType` | `catalog_type_contract` | yes | `src/api/models/catalogs/contract_type.py` |
| `DocumentType` | `catalog_type_document` | yes | `src/api/models/catalogs/document_type.py` |
| `EmailType` | `catalog_type_email` | yes | `src/api/models/catalogs/email_type.py` |
| `PhoneType` | `catalog_type_phones` | yes | `src/api/models/catalogs/phone_type.py` |
| `CatalogZipCode` | `catalog_zip_code` | yes | `src/api/models/catalogs/zip_code.py` |
| `PilouContract` | `Contract` | no | `src/api/models/pilou_models.py` |
| `Contract` | `contracts` | yes | `src/api/models/contract.py` |
| `Country` | `countries` | yes | `src/api/models/catalogs/country.py` |
| `Csf` | `CSF` | no | `src/api/models/pilou_models.py` |
| `Document` | `document` | yes | `src/api/models/document.py` |
| `Economicactivity` | `EconomicActivity` | no | `src/api/models/pilou_models.py` |
| `EngagementClient` | `engagement_client` | yes | `src/api/models/engagement_client.py` |
| `FileImport` | `file_imports` | yes | `src/api/models/file_import.py` |
| `FriendCodes` | `friend_codes` | no | `src/api/models/legacy.py` |
| `InvestorProfiles` | `investor_profiles` | no | `src/api/models/legacy.py` |
| `Investorprofile` | `InvestorProfile` | no | `src/api/models/pilou_models.py` |
| `PilouKyc` | `Kyc` | no | `src/api/models/pilou_models.py` |
| `Kycs` | `kycs` | no | `src/api/models/legacy.py` |
| `LeadToFileImport` | `lead_to_file_imports` | yes | `src/api/models/lead_to_file_import.py` |
| `Movements` | `movements` | no | `src/api/models/movement.py` |
| `Pep` | `Pep` | no | `src/api/models/pilou_models.py` |
| `Person` | `person` | yes | `src/api/models/person.py` |
| `Phone` | `phones` | yes | `src/api/models/phone.py` |
| `PldReport` | `pld_report` | yes | `src/api/models/pld_report.py` |
| `Provider` | `provider` | yes | `src/api/models/provider.py` |
| `Quizanswer` | `QuizAnswer` | no | `src/api/models/pilou_models.py` |
| `Quizquestion` | `QuizQuestion` | no | `src/api/models/pilou_models.py` |
| `RiskProfile` | `risk_profile` | yes | `src/api/models/catalogs/risk_profile.py` |
| `Source` | `sources` | yes | `src/api/models/source.py` |
| `State` | `states` | yes | `src/api/models/catalogs/state.py` |
| `Tax` | `taxes` | yes | `src/api/models/tax.py` |
| `PilouUser` | `User` | no | `src/api/models/pilou_models.py` |
| `UserBrockerageHouses` | `user_brockerage_houses` | no | `src/api/models/legacy.py` |
| `UserBrokerAccount` | `UserBrokerAccount` | no | `src/api/models/pilou_models.py` |
| `LegacyUser` | `users` | no | `src/api/models/legacy.py` |

### onboarding-webapp

| Model | Effective table | Managed | Source |
|---|---|---:|---|
| `action_text_rich_texts` | `action_text_rich_texts` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `active_storage_attachments` | `active_storage_attachments` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `active_storage_blobs` | `active_storage_blobs` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `active_storage_variant_records` | `active_storage_variant_records` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `Address` | `Address` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `admins` | `admins` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `AnonymousComplaint` | `AnonymousComplaint` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `ar_internal_metadata` | `ar_internal_metadata` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `BankAccount` | `BankAccount` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `Beneficiary` | `Beneficiary` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `blog_categories` | `blog_categories` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `blogs` | `blogs` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `boolean_answers` | `boolean_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `brockerage_houses` | `brockerage_houses` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `Broker` | `Broker` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `catalog_tickers` | `catalog_tickers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `cetes` | `cetes` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `CompanyPartnership` | `CompanyPartnership` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `Contract` | `Contract` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `ContractNumber` | `ContractNumber` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `CSF` | `CSF` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `date_answers` | `date_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `datetime_answers` | `datetime_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `documents` | `documents` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `early_accesses` | `early_accesses` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `EconomicActivity` | `EconomicActivity` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `equities` | `equities` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `Event` | `Event` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `field_options` | `field_options` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `fields` | `fields` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `file_answers` | `file_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `float_answers` | `float_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `form_sections` | `form_sections` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `forms` | `forms` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `friend_codes` | `friend_codes` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `gbm_balances` | `gbm_balances` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `gbm_movements` | `gbm_movements` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `GBMManualManagementType` | `GBMManualManagementType` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `history_exchanges` | `history_exchanges` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `InitialTransactionalProfile` | `InitialTransactionalProfile` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `integer_answers` | `integer_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `investment_plan_users` | `investment_plan_users` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `investment_plans` | `investment_plans` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `InvestmentPlan` | `InvestmentPlan` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `investor_profiles` | `investor_profiles` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `InvestorProfile` | `InvestorProfile` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `Kyc` | `Kyc` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `kycs` | `kycs` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `movements` | `movements` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `option_answers` | `option_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `options` | `options` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `OriginalSourceCatalog` | `OriginalSourceCatalog` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `Pep` | `Pep` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `pilou_docs` | `pilou_docs` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `plan_features` | `plan_features` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `plan_plan_features` | `plan_plan_features` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `plans` | `plans` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `PLDReport` | `PLDReport` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `portfolios` | `portfolios` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `profile_investments` | `profile_investments` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `Quiz` | `Quiz` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `QuizAnswer` | `QuizAnswer` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `QuizQuestion` | `QuizQuestion` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `schema_migrations` | `schema_migrations` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `section_fields` | `section_fields` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `sections` | `sections` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `text_answers` | `text_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `tickers` | `tickers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `time_answers` | `time_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `User` | `User` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `user_brockerage_houses` | `user_brockerage_houses` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `user_form_section_field_answers` | `user_form_section_field_answers` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `user_form_section_fields` | `user_form_section_fields` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `user_form_sections` | `user_form_sections` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `user_forms` | `user_forms` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `user_movements` | `user_movements` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |
| `UserBrokerAccount` | `UserBrokerAccount` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `UserErrors` | `UserErrors` | yes | `libs/api/shared/data-access-warehouse/src/lib/schema.prisma` |
| `users` | `users` | yes | `libs/api/shared/data-access-warehouse/src/lib/legacy.schema.prisma` |

