require "test_helper"

class UserTest < ActiveSupport::TestCase
  test "authenticates with a valid password" do
    user = User.create!(email: "admin@example.com", password: "s3cretpass")

    assert user.valid_password?("s3cretpass")
    assert_not user.valid_password?("wrong")
  end

  test "requires an email" do
    user = User.new(password: "s3cretpass")

    assert_not user.valid?
  end
end
