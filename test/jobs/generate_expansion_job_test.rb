require "test_helper"

# Minitest 6 dropped Minitest::Mock, so a `.stub`-style helper isn't available
# by default. This helper is named `stub_class_method` (not `stub`) so it can't
# collide with the `Object#stub` polyfill in
# test/services/claude_expand_service_test.rb: both files previously guarded
# their monkeypatch with `unless Object.method_defined?(:stub)`, which made
# whichever file loaded first win globally for the whole test process — and
# that file's `.stub` unconditionally does `singleton_class.remove_method`,
# which would permanently delete ExpansionProcessor.process (defined directly
# on its singleton class, with no ancestor fallback) for the rest of the run.
# Using a distinct method name removes the race entirely rather than relying
# on load order.
unless Object.method_defined?(:stub_class_method)
  class Object
    def stub_class_method(method_name, callable, &block)
      has_original = singleton_class.method_defined?(method_name) || singleton_class.private_method_defined?(method_name)
      original = singleton_class.instance_method(method_name) if has_original

      singleton_class.define_method(method_name) do |*args, &method_block|
        callable.call(*args, &method_block)
      end
      block.call
    ensure
      singleton_class.remove_method(method_name)
      singleton_class.define_method(method_name, original) if original
    end
  end
end

class GenerateExpansionJobTest < ActiveJob::TestCase
  setup do
    @user = User.create!(email: "job@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(file_name: "notes.md", selected_text: "beta", occurrence: 0, question: "why?")
  end

  test "completes a pending expansion once" do
    with_processor(->(_) { "/notes--expand-1.html" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
      GenerateExpansionJob.perform_now(@expansion.id)
    end

    assert_equal ["completed", "/notes--expand-1.html"], @expansion.reload.attributes.values_at("status", "url")
  end

  test "stamps job_started after claiming the expansion" do
    with_processor(->(_) { "/notes--expand-1.html" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
    end

    assert_kind_of Integer, @expansion.reload.timings["job_started"]
  end

  test "stores safe details for known and unexpected failures" do
    with_processor(->(_) { raise ClaudeExpandService::Error, "token leaked" }) do
      GenerateExpansionJob.perform_now(@expansion.id)
    end
    assert_equal ["failed", "Generation failed."], @expansion.reload.attributes.values_at("status", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "why?")
    with_processor(->(_) { raise SelectionLinker::NotFound, "Selection not found in source — select a plainer run of text." }) do
      GenerateExpansionJob.perform_now(failed.id)
    end
    assert_equal ["failed", "Selection not found in source — select a plainer run of text."], failed.reload.attributes.values_at("status", "error_detail")
  end

  test "reports a truncated rewrite to the reader" do
    expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", question: "why?", mode: "edit_in_place"
    )
    ExpansionProcessor.stub_class_method(:process, ->(*) { raise ExpansionProcessor::TruncatedRewrite, "Rewrite looked truncated." }) do
      GenerateExpansionJob.perform_now(expansion.id)
    end

    assert_equal "failed", expansion.reload.status
    assert_equal "Rewrite looked truncated.", expansion.error_detail
  end

  private

  def with_processor(callable)
    original = ExpansionProcessor.method(:process)
    ExpansionProcessor.define_singleton_method(:process, &callable)
    yield
  ensure
    ExpansionProcessor.define_singleton_method(:process, original)
  end
end
