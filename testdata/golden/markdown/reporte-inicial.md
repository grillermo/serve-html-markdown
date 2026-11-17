## Propósito de la reunión y contexto estratégico

El objetivo central de la reunión fue evaluar el estado de desarrollo de la aplicación móvil de Pillow, identificando riesgos, bloqueos y el progreso respecto al cronograma comprometido. Este proyecto es considerado fundamental para la narrativa de cara a la búsqueda de inversión, ya que demostrar la capacidad de innovación tecnológica y el potencial de escalar el negocio es un requisito clave para los próximos tres meses.

- **Evaluación del progreso y alineación con objetivos de inversión**: La aplicación móvil no es solo una nueva funcionalidad, sino un elemento central para convencer a inversionistas. Se necesita una historia convincente que demuestre cómo la tecnología permitirá escalar el negocio, y para ello es imperativo tener la aplicación publicada y operativa en el plazo establecido.
- **Definición del alcance y expectativas**: Se reiteró que el rol de consultoría tiene una duración inicial de tres meses, con el éxito condicionado a la entrega de esta aplicación. Esto subraya la urgencia y la importancia de proteger el foco del equipo de desarrollo en este proyecto por encima de otras demandas.

## Distribución del tiempo y desafíos operativos

Se analizó en detalle cómo el equipo de desarrollo distribuye su tiempo entre el mantenimiento de sistemas existentes, peticiones operativas y el desarrollo de la nueva aplicación móvil, identificando que, aunque la situación es manejable, existen desvíos que requieren gestión activa.

- **Tiempo dedicado a infraestructura y mantenimiento**: Aproximadamente el 40% del tiempo se destina a tareas de infraestructura, como despliegues, renovación de certificados y mantenimiento general de servidores. Una vez configurados, los sistemas suelen ser estables, pero estas tareas son recurrentes.
- **Gestión de peticiones operativas y de cumplimiento**: El equipo operativo realiza solicitudes constantes (por ejemplo, reportes de comisiones o integraciones para *compliance*) que, aunque ahora se priorizan a través de un proceso formalizado con Jira, siguen representando una carga variable. La reciente incorporación de un oficial de cumplimiento podría generar nuevas peticiones, aunque su impacto aún no está claro.
- **Capacidad real para el desarrollo de la nueva app**: Se estima que, en una semana típica, quedan entre 3 y 4 días (aproximadamente 20-30 horas) para dedicar exclusivamente al desarrollo de la aplicación Pillow. Sin embargo, este tiempo puede verse reducido por urgencias no planificadas o cambios de prioridades, como sucedió cuando se pidió desarrollar todas las vistas de golpe para presentarlas a un diseñador.

## Estado actual del desarrollo de la aplicación Pillow

El desarrollo de la aplicación móvil (Pillow App) avanza en dos frentes: un backend en Django y un frontend en React Native, con un MVP que busca replicar las funcionalidades básicas del dashboard web existente.

- **Definición del MVP y su alcance**: La primera etapa consiste en replicar el dashboard actual en una versión móvil nativa. Este dashboard es principalmente de solo lectura, mostrando saldos, rendimientos y el perfil del usuario, ya que los clientes no necesitan modificar datos directamente. La decisión de empezar con este scope se basó en la petición de los clientes de tener una experiencia móvil más práctica.
- **Progreso del desarrollo y dependencias técnicas**: El backend ya está hosteado en GCP, mientras que el frontend aún no está desplegado. Aproximadamente se ha completado entre un 20% y un 30% del trabajo total planificado. Una decisión técnica crítica pendiente es definir la fuente de datos: si crear una nueva base de datos sincronizada o exponer endpoints desde el *backoffice* existente para evitar multiplicar fuentes de información.
- **Falta de diseño final como riesgo**: La ausencia de diseños UI/UX finalizados, tras descartar trabajar con agencias de diseño externas y depender de herramientas de IA, se identifica como un riesgo que podría retrasar el desarrollo frontend una vez que las decisiones técnicas estén resueltas.

## Arquitectura e infraestructura existente

La arquitectura de Pillow es modular pero fragmentada, compuesta por múltiples servicios y bases de datos que han ido evolucionando con el tiempo, lo que añade complejidad al desarrollo de la nueva aplicación.

- **Panorama de servicios y hospedaje**: Existen varios servicios independientes: el *backoffice* (Django), el dashboard legado (en Heroku), el proceso de onboarding (Next.js/React), un servicio unificador de proveedores (Pillow Provider), un módulo de PLD y servicios FTP para tareas programadas (crons). La mayoría han sido migrados de AWS a GCP para aprovechar créditos de la plataforma.
- **Gestión de datos y clientes**: Hay una coexistencia de clientes *legacy* y nuevos, con información distribuida en distintas bases de datos. El *backoffice* intenta consolidar estos datos, pero la nueva app móvil requiere una estrategia clara para acceder a la información sin crear dependencias problemáticas o duplicar esfuerzos.
- **Entornos de desarrollo y despliegue**: Se utilizan entornos de staging (DEV) y producción (PRO) claramente separados en GCP. El flujo de integración se ha optimizado recientemente con un pipeline de CI/CD para algunos proyectos, automatizando los despliegues.

## Métricas de validación y justificación del proyecto móvil

Se discutió la necesidad de basar la estrategia de la app móvil en datos concretos sobre el comportamiento actual de los usuarios, para justificar la inversión y definir metas medibles de éxito.

- **Falta de datos analíticos robustos**: El dashboard actual carece de Google Analytics o un sistema similar, por lo que es difícil conocer con precisión la proporción de usuarios móviles versus de escritorio, o la frecuencia de acceso. Esta es una brecha importante para validar la hipótesis de que una app nativa incrementará el compromiso.
- **Fuentes de datos alternativas y limitaciones**: Se identificó que Cloudflare proporciona datos de tráfico web, pero solo retiene información de los últimos 30 días. Los datos preliminares sugieren que alrededor del 20% del tráfico ya proviene de dispositivos móviles. Se propuso implementar una herramienta como PostHog en la nueva aplicación para obtener análisis más profundos y *feature flags*.
- **Definición del éxito más allá del MVP**: El éxito de la app no se medirá solo por replicar funcionalidades, sino por su capacidad para crear un *hábito financiero* en los usuarios. El objetivo final es aumentar la frecuencia con la que los clientes interactúan con sus inversiones, lo que idealmente se traduciría en más depósitos y una mayor lealtad. La inmediatez y la facilidad de acceso (como el login con huella o Face ID) son factores clave para lograr esto.

## Uso de inteligencia artificial en el desarrollo

La adopción de herramientas de IA para el desarrollo de código es tanto una práctica operativa como un elemento estratégico de la narrativa de Pillow como empresa "AI-native" de cara a los inversionistas.

- **Herramientas en uso y necesidad de recursos empresariales**: El desarrollo frontend se está realizando intensivamente con herramientas como Cursor y Claude, pero utilizando créditos personales de los desarrolladores. Se reconoce la necesidad urgente de proporcionar cuentas empresariales (como un plan de Claude por 100 USD/mes) para separar costos y escalar su uso de forma oficial.
- **La IA como parte de la estrategia de inversión**: Utilizar IA en el proceso de desarrollo no es solo una cuestión de eficiencia; es un mensaje clave para los potenciales inversionistas. Demostrar que Pillow integra estas tecnologías de punta en su operación diaria fortalece su imagen como una startup innovadora y preparada para el futuro.
- **Enfoque recomendado para el desarrollo con IA**: Se sugirió que, ante la falta de diseños finales, el tiempo más valioso se podría invertir en investigar y establecer una arquitectura de código sólida y patrones claros para la app en React Native. Esto proporcionaría un contexto de alta calidad para las herramientas de IA, permitiéndoles generar código más consistente y reduciendo la deuda técnica, incluso antes de tener las vistas definitivas.
