class GenerateExpansionJob < ApplicationJob
  queue_as :default

  def perform(expansion_id)
    expansion = Expansion.find_by(id: expansion_id)
    return unless expansion&.claim!

    expansion.complete!(ExpansionProcessor.process(expansion))
  rescue ClaudeExpandService::Error
    Rails.logger.error("Expansion generation failed for job #{expansion_id}")
    expansion&.fail!("Generation failed.")
  rescue SelectionLinker::Error, ActionController::BadRequest,
         ResolvesServedFiles::UnsupportedFile, ResolvesServedFiles::MissingFile => error
    expansion&.fail!(error.message)
  rescue StandardError => error
    Rails.logger.error("Expansion job #{expansion_id} failed: #{error.class}")
    expansion&.fail!("Expansion failed.")
  end
end
